package router

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sync"

	"github.com/coreos/go-iptables/iptables"
	"github.com/gravitl/netclient/ncutils"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/models"
)

func isIptablesSupported() bool {
	_, err4 := exec.LookPath("iptables")
	_, err6 := exec.LookPath("ip6tables")
	return err4 == nil && err6 == nil
}

// constants needed to manage and create iptable rules
const (
	ipv6                = "ipv6"
	ipv4                = "ipv4"
	defaultIpTable      = "filter"
	netmakerFilterChain = "netmakerfilter"
	defaultNatTable     = "nat"
	netmakerNatChain    = "netmakernat"
	iptableFWDChain     = "FORWARD"
	nattablePRTChain    = "POSTROUTING"
)

var (

	// filter table netmaker jump rule
	filterNmJumpRules = []ruleInfo{
		{
			rule:  []string{"-i", ncutils.GetInterfaceName(), "-j", netmakerFilterChain},
			table: defaultIpTable,
			chain: iptableFWDChain,
		},
		{
			rule:  []string{"-i", ncutils.GetInterfaceName(), "-j", "DROP"},
			table: defaultIpTable,
			chain: netmakerFilterChain,
		},
		{
			rule:  []string{"-i", ncutils.GetInterfaceName(), "-j", "RETURN"},
			table: defaultIpTable,
			chain: netmakerFilterChain,
		},
	}
	natNmJumpRules = []ruleInfo{
		{
			rule:  []string{"-o", ncutils.GetInterfaceName(), "-j", netmakerNatChain},
			table: defaultNatTable,
			chain: nattablePRTChain,
		},
		{
			rule:  []string{"-j", "RETURN"},
			table: defaultNatTable,
			chain: netmakerNatChain,
		},
	}
)

type ruleInfo struct {
	rule  []string
	table string
	chain string
}

type iptablesManager struct {
	ctx          context.Context
	stop         context.CancelFunc
	ipv4Client   *iptables.IPTables
	ipv6Client   *iptables.IPTables
	ingRules     serverrulestable
	defaultRules ruletable
	mux          sync.Mutex
}

func createChain(iptables *iptables.IPTables, table, newChain string) error {

	chains, err := iptables.ListChains(table)
	if err != nil {
		return fmt.Errorf("couldn't get %s %s table chains, error: %v", iptablesProtoToString(iptables.Proto()), table, err)
	}

	shouldCreateChain := true
	for _, chain := range chains {
		if chain == newChain {
			shouldCreateChain = false
		}
	}

	if shouldCreateChain {
		err = iptables.NewChain(table, newChain)
		if err != nil {
			return fmt.Errorf("couldn't create %s chain %s in %s table, error: %v", iptablesProtoToString(iptables.Proto()), newChain, table, err)
		}

	}
	return nil
}

// CleanRoutingRules cleans existing iptables resources that we created by the agent
func (i *iptablesManager) CleanRoutingRules(server, ruleTableName string) {
	ruleTable := i.FetchRuleTable(server, ruleTableName)
	i.mux.Lock()
	defer i.mux.Unlock()
	for _, rulesCfg := range ruleTable {
		for _, rules := range rulesCfg.rulesMap {
			iptablesClient := i.ipv4Client
			if !rulesCfg.isIpv4 {
				iptablesClient = i.ipv6Client
			}
			for _, rule := range rules {
				iptablesClient.DeleteIfExists(rule.table, rule.chain, rule.rule...)
			}
		}
	}

}

// CreateChains - creates default chains and rules
func (i *iptablesManager) CreateChains() error {
	i.mux.Lock()
	defer i.mux.Unlock()
	// remove jump rules
	i.removeJumpRules()
	i.cleanup(defaultIpTable, netmakerFilterChain)
	i.cleanup(defaultNatTable, netmakerNatChain)

	//errMSGFormat := "iptables: failed creating %s chain %s,error: %v"

	err := createChain(i.ipv4Client, defaultIpTable, netmakerFilterChain)
	if err != nil {
		logger.Log(1, "failed to create netmaker chain: ", err.Error())
		return err
	}
	err = createChain(i.ipv4Client, defaultNatTable, netmakerNatChain)
	if err != nil {
		logger.Log(1, "failed to create netmaker chain: ", err.Error())
		return err
	}

	err = createChain(i.ipv6Client, defaultIpTable, netmakerFilterChain)
	if err != nil {
		logger.Log(1, "failed to create netmaker chain: ", err.Error())
		return err
	}
	err = createChain(i.ipv6Client, defaultNatTable, netmakerNatChain)
	if err != nil {
		logger.Log(1, "failed to create netmaker chain: ", err.Error())
		return err
	}
	// add jump rules
	i.addJumpRules()
	return nil
}

func (i *iptablesManager) addJumpRules() {
	for _, rule := range filterNmJumpRules {
		err := i.ipv4Client.Append(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", rule.rule, err.Error()))
		}
		err = i.ipv6Client.Append(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", rule.rule, err.Error()))
		}
	}
	for _, rule := range natNmJumpRules {
		err := i.ipv4Client.Append(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", rule.rule, err.Error()))
		}
		err = i.ipv6Client.Append(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", rule.rule, err.Error()))
		}
	}

}
func (i *iptablesManager) removeJumpRules() {
	for _, rule := range filterNmJumpRules {
		err := i.ipv4Client.DeleteIfExists(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to rm rule: %v, Err: %v ", rule.rule, err.Error()))
		}
		err = i.ipv6Client.DeleteIfExists(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to rm rule: %v, Err: %v ", rule.rule, err.Error()))
		}
	}
	for _, rule := range natNmJumpRules {
		err := i.ipv4Client.DeleteIfExists(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to rm rule: %v, Err: %v ", rule.rule, err.Error()))
		}
		err = i.ipv6Client.DeleteIfExists(rule.table, rule.chain, rule.rule...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to rm rule: %v, Err: %v ", rule.rule, err.Error()))
		}
	}
}

func (i *iptablesManager) AddIngressRoutingRule(server, extPeerKey string, peerInfo models.PeerExtInfo) error {
	ruleTable := i.FetchRuleTable(server, ingressTable)
	defer i.SaveRules(server, ingressTable, ruleTable)
	i.mux.Lock()
	defer i.mux.Unlock()
	prefix, err := netip.ParsePrefix(peerInfo.PeerAddr.String())
	if err != nil {
		return err
	}
	iptablesClient := i.ipv4Client
	if prefix.Addr().Unmap().Is6() {
		iptablesClient = i.ipv6Client
	}

	ruleSpec := []string{"-d", peerInfo.PeerAddr.String(), "-j", "ACCEPT"}
	err = iptablesClient.Insert(defaultIpTable, netmakerFilterChain, 1, ruleSpec...)
	if err != nil {
		logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", ruleSpec, err.Error()))
	}
	ruleTable[extPeerKey].rulesMap[peerInfo.PeerKey] = []ruleInfo{
		{
			rule:  ruleSpec,
			chain: netmakerFilterChain,
			table: defaultIpTable,
		},
	}
	return nil
}

// InsertIngressRoutingRules inserts an iptables rule pair to the netmaker chain and if enabled, to the nat chain
func (i *iptablesManager) InsertIngressRoutingRules(server string, extinfo models.ExtClientInfo) error {
	ruleTable := i.FetchRuleTable(server, ingressTable)
	defer i.SaveRules(server, ingressTable, ruleTable)
	i.mux.Lock()
	defer i.mux.Unlock()
	logger.Log(0, "Adding Ingress Rules For Ext. Client: ", extinfo.ExtPeerKey)
	prefix, err := netip.ParsePrefix(extinfo.ExtPeerAddr.String())
	if err != nil {
		return err
	}
	isIpv4 := true
	iptablesClient := i.ipv4Client
	if prefix.Addr().Unmap().Is6() {
		iptablesClient = i.ipv6Client
		isIpv4 = false
	}

	ruleTable[extinfo.ExtPeerKey] = rulesCfg{
		isIpv4:   isIpv4,
		rulesMap: make(map[string][]ruleInfo),
	}

	for _, peerInfo := range extinfo.Peers {
		if !peerInfo.Allow {
			continue
		}
		ruleSpec := []string{"-s", extinfo.ExtPeerAddr.String(), "-d", peerInfo.PeerAddr.String(), "-j", "ACCEPT"}
		logger.Log(2, fmt.Sprintf("-----> adding rule: %+v", ruleSpec))
		err := iptablesClient.Insert(defaultIpTable, netmakerFilterChain, 1, ruleSpec...)
		if err != nil {
			logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", ruleSpec, err.Error()))
		}
		ruleTable[extinfo.ExtPeerKey].rulesMap[peerInfo.PeerKey] = []ruleInfo{
			{

				rule:  ruleSpec,
				chain: netmakerFilterChain,
				table: defaultIpTable,
			},
		}

	}
	if !extinfo.Masquerade {
		return nil
	}
	// iptables -t nat -A netmakernat  -s 10.24.52.252/32 -o netmaker -j MASQUERADE
	// iptables -t nat -A netmakernat -d 10.24.52.252/32 -o netmaker -j MASQUERADE
	ruleSpec := []string{"-s", extinfo.ExtPeerAddr.String(), "-o", "netmaker", "-j", "MASQUERADE"}
	logger.Log(2, fmt.Sprintf("----->[NAT] adding rule: %+v", ruleSpec))
	err = iptablesClient.Insert(defaultNatTable, netmakerNatChain, 1, ruleSpec...)
	if err != nil {
		logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", ruleSpec, err.Error()))
	}
	routes := ruleTable[extinfo.ExtPeerKey].rulesMap[extinfo.ExtPeerKey]
	routes = append(routes, ruleInfo{
		rule:  ruleSpec,
		table: defaultNatTable,
		chain: netmakerNatChain,
	})
	ruleSpec = []string{"-d", extinfo.ExtPeerAddr.String(), "-o", "netmaker", "-j", "MASQUERADE"}
	logger.Log(2, fmt.Sprintf("----->[NAT] adding rule: %+v", ruleSpec))
	err = iptablesClient.Insert(defaultNatTable, netmakerNatChain, 1, ruleSpec...)
	if err != nil {
		logger.Log(1, fmt.Sprintf("failed to add rule: %v, Err: %v ", ruleSpec, err.Error()))
	}
	routes = append(routes, ruleInfo{
		rule:  ruleSpec,
		table: defaultNatTable,
		chain: netmakerNatChain,
	})
	ruleTable[extinfo.ExtPeerKey].rulesMap[extinfo.ExtPeerKey] = routes

	return nil
}

func (i *iptablesManager) cleanup(table, chain string) {

	err := i.ipv4Client.ClearAndDeleteChain(table, chain)
	if err != nil {
		logger.Log(0, "[ipv4] failed to clear chain: ", table, chain, err.Error())
	}
	err = i.ipv6Client.ClearAndDeleteChain(table, chain)
	if err != nil {
		logger.Log(0, "[ipv6] failed to clear chain: ", table, chain, err.Error())
	}
}

func (i *iptablesManager) FetchRuleTable(server string, tableName string) ruletable {
	i.mux.Lock()
	defer i.mux.Unlock()
	var rules ruletable
	switch tableName {
	case ingressTable:
		rules = i.ingRules[server]
		if rules == nil {
			rules = make(ruletable)
		}
	}
	return rules
}

func (i *iptablesManager) SaveRules(server, tableName string, rules ruletable) {
	i.mux.Lock()
	defer i.mux.Unlock()
	logger.Log(1, "Saving rules to table: ", tableName)
	switch tableName {
	case ingressTable:
		i.ingRules[server] = rules
	}
}

// RemoveRoutingRules removes an iptables rule pair from forwarding and nat chains
func (i *iptablesManager) RemoveRoutingRules(server, ruletableName, peerKey string) error {
	rulesTable := i.FetchRuleTable(server, ruletableName)
	defer i.SaveRules(server, ruletableName, rulesTable)
	i.mux.Lock()
	defer i.mux.Unlock()
	if _, ok := rulesTable[peerKey]; !ok {
		return errors.New("peer not found in rule table: " + peerKey)
	}
	iptablesClient := i.ipv4Client
	if !rulesTable[peerKey].isIpv4 {
		iptablesClient = i.ipv6Client
	}

	for _, rules := range rulesTable[peerKey].rulesMap {
		for _, rule := range rules {
			err := iptablesClient.DeleteIfExists(rule.table, rule.chain, rule.rule...)
			if err != nil {
				return fmt.Errorf("iptables: error while removing existing %s rules [%v] for %s: %v",
					rule.table, rule.rule, peerKey, err)
			}
		}

	}
	delete(rulesTable, peerKey)
	return nil
}

// RemoveRoutingRules removes an iptables rule pair from forwarding and nat chains
func (i *iptablesManager) DeleteRoutingRule(server, ruletableName, srcPeerKey, dstPeerKey string) error {
	rulesTable := i.FetchRuleTable(server, ruletableName)
	defer i.SaveRules(server, ruletableName, rulesTable)
	i.mux.Lock()
	defer i.mux.Unlock()
	if _, ok := rulesTable[srcPeerKey]; !ok {
		return errors.New("peer not found in rule table: " + srcPeerKey)
	}
	iptablesClient := i.ipv4Client
	if !rulesTable[srcPeerKey].isIpv4 {
		iptablesClient = i.ipv6Client
	}
	if rules, ok := rulesTable[srcPeerKey].rulesMap[dstPeerKey]; ok {
		for _, rule := range rules {
			err := iptablesClient.DeleteIfExists(rule.table, rule.chain, rule.rule...)
			if err != nil {
				return fmt.Errorf("iptables: error while removing existing %s rules [%v] for %s: %v",
					rule.table, rule.rule, srcPeerKey, err)
			}
		}
	} else {
		return errors.New("rules not found for: " + dstPeerKey)
	}

	return nil
}

func (i *iptablesManager) FlushAll() {
	i.mux.Lock()
	defer i.mux.Unlock()
	// remove jump rules
	i.removeJumpRules()
	i.cleanup(defaultIpTable, netmakerFilterChain)
	i.cleanup(defaultNatTable, netmakerNatChain)
}

func iptablesProtoToString(proto iptables.Protocol) string {
	if proto == iptables.ProtocolIPv6 {
		return ipv6
	}
	return ipv4
}
