package service

import (
	"net/http"
	"reflect"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
)

func TestSelectedClashEntranceGetsSubscriptionWithoutManualNodeSelection(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	a := model.GostRule{Name: "Clash source", NodeID: &entry.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP,
		ListenIP: "127.0.0.1", ListenPort: 10951, UseClashRuleset: true}
	b := model.GostRule{Name: "relay", NodeID: &exit.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP, ListenPort: 10952}
	saveServiceFixtures(t, db, &a, &b)
	request := &dto.CreateTunnelReq{Name: "transparent", EntryRuleID: a.ID, ExitRuleID: b.ID}
	tunnels := NewTunnelService(db)
	_, err := tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleClashUnavailable)
	ruleset := model.GostRuleset{ID: gostRulesetID, URL: "https://example.test/rules", Matchers: []string{".old.test"}}
	saveServiceFixtures(t, db, &ruleset)
	entryRemote.mu.Lock()
	entryRemote.reject = map[string]int{"POST /config/bypasses": http.StatusBadGateway}
	entryRemote.mu.Unlock()
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleChainCreateFailed)
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "POST /config/bypasses")
	entryRemote.mu.Unlock()
	tunnel, err := tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	forward := entryRemote.service(tunnel.EntryRule.ServiceID)
	clash := entryRemote.chain(forward.Handler.Chain)
	relay := exitRemote.service(tunnel.ServiceID)
	if forward.Addr != "127.0.0.1:10951" || relay.Addr != ":10952" || forward.Handler.Chain != clashTunnelChainName(tunnel.ID) ||
		*clash.Hops[0].Nodes[0].Connector.Auth != *relay.Handler.Auth {
		t.Fatal("selected Clash source and destination do not use the same authenticated Relay")
	}
	ruleset.Matchers = []string{".new.test"}
	requireServiceError(t, db.Save(&ruleset).Error, nil)
	entryRemote.mu.Lock()
	entryRemote.reject["POST /config?format=yaml"] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	subscriptions := NewGostRulesetService(db)
	unsaved := make(map[uint]bool)
	subscriptions.sync(t.Context(), unsaved)
	var matchers gost.AdmissionConfig
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !reflect.DeepEqual(matchers.Matchers, ruleset.Matchers) || !unsaved[entry.ID] {
		t.Fatalf("subscription reached the selected entry but must be saved again: %+v, %v", matchers, unsaved)
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "POST /config?format=yaml")
	entryRemote.mu.Unlock()
	subscriptions.sync(t.Context(), unsaved)
	if unsaved[entry.ID] {
		t.Fatal("retry did not save the changed subscription")
	}
}

func TestClashEntranceUsesSeparateChainAndFollowsExit(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry := startServiceTestNode(t, entryRemote)
	exit := startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	tunnels, rules := NewTunnelService(db), NewRuleService(db)
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{
		Name: "route A to B", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 8443,
	}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	tunnel, err = tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)

	request := dto.CreateRuleReq{
		Name: "local transparent TCP", TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "tcp",
		ListenIP: "127.0.0.1", ListenPort: 443, UseClashRuleset: true,
	}
	for _, invalid := range []dto.CreateRuleReq{
		{TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: 443, UseClashRuleset: true},
		{TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "tcp", ListenIP: "192.0.2.3", ListenPort: 443, UseClashRuleset: true},
		{TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: 443, UseClashRuleset: true, Targets: []string{"fixed.test:443"}},
	} {
		_, err = rules.Create(&invalid, testAdmin, testIP, testAgent)
		requireServiceError(t, err, bizerrors.ErrRuleClashInvalid)
	}
	rule, err := rules.Create(&request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleClashUnavailable)
	ruleset := &model.GostRuleset{ID: gostRulesetID, URL: "https://example.test/list", Matchers: []string{".first.test"}}
	saveServiceFixtures(t, db, ruleset)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), nil)
	rule, err = rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	service := entryRemote.service(rule.ServiceID)
	if rule.Status != model.RuleStatusRunning || service.Listener == nil || service.Handler == nil ||
		service.Addr != "127.0.0.1:443" || service.Listener.Type != "red" || service.Handler.Type != "red" ||
		service.Handler.Chain != clashTunnelChainName(tunnel.ID) || service.Forwarder != nil ||
		service.SockOpts == nil || service.SockOpts.Mark != gost.ClashRedirectMark || service.Handler.Metadata["sniffing"] != true {
		t.Fatalf("transparent rule and Gost service: %+v, %+v", rule, service)
	}
	var matchers gost.AdmissionConfig
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !matchers.Whitelist || !reflect.DeepEqual(matchers.Matchers, ruleset.Matchers) {
		t.Fatalf("initial Gost matchers: %+v", matchers)
	}
	// The next subscription update reaches the rule's entry without a manually selected node.
	ruleset.Matchers = []string{"192.0.2.0/24"}
	requireServiceError(t, db.Save(ruleset).Error, nil)
	NewGostRulesetService(db).sync(t.Context(), make(map[uint]bool))
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !reflect.DeepEqual(matchers.Matchers, ruleset.Matchers) {
		t.Fatalf("updated Gost matchers: %+v", matchers)
	}
	base := entryRemote.chain(tunnel.ChainID)
	clash := entryRemote.chain(service.Handler.Chain)
	NewRuleSyncService(db).syncTunnels(utils.GetGostClient(&entry), entry.ID, []model.GostTunnel{*tunnel},
		[]gost.ChainConfig{base, clash}, map[uint]string{exit.ID: "198.51.100.20"})
	for _, name := range []string{base.Name, clash.Name} {
		addr, found := entryRemote.chain(name).TunnelRelayAddress()
		if !found || addr != "198.51.100.20:8443" {
			t.Errorf("updated route %q = %q, %t", name, addr, found)
		}
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if entryRemote.chain(service.Handler.Chain).Name != "" {
		t.Fatal("stopping tunnel did not remove its Clash chain")
	}
}
