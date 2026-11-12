package service

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
)

func newOriginalTargetTestTunnel(t *testing.T) *transparentTestTunnel {
	t.Helper()
	f := newTransparentTestTunnel(t)
	f.source.Role, f.source.Protocol, f.source.UseClashRuleset = model.EndpointRoleEntry, model.RuleProtocolTransparent, false
	f.destination.Role, f.destination.UseOriginalTarget = model.EndpointRoleExit, true
	requireServiceError(t, f.db.Model(&f.source).Select("Role", "Protocol", "UseClashRuleset").Updates(&f.source).Error, nil)
	requireServiceError(t, f.db.Model(&f.destination).Select("Role", "UseOriginalTarget").Updates(&f.destination).Error, nil)
	requireServiceError(t, f.db.Delete(&model.GostRuleset{}, gostRulesetID).Error, nil)
	return f
}

func TestTransparentIngressForwardsAllOriginalTargetsWithoutRuleset(t *testing.T) {
	f := newOriginalTargetTestTunnel(t)
	requireServiceError(t, f.start(), nil)
	stored, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	for _, name := range []string{entranceServiceName(f.source.ID), dockerEntranceServiceName(f.source.ID)} {
		svc := f.entryNode.service(name)
		if svc.Listener.Type != "red" || svc.Handler.Type != "red" || svc.Handler.Metadata["sniffing"] != nil || svc.Forwarder != nil ||
			svc.SockOpts == nil || svc.SockOpts.Mark != int(f.entryNode.mark) || svc.Handler.Chain != clashTunnelChainName(stored.ID) {
			t.Fatalf("all-target transparent service = %+v", svc)
		}
	}
	var derived struct {
		Hops []struct {
			Bypass   string
			SockOpts gost.SocketOptions
			Nodes    []struct{ SockOpts gost.SocketOptions }
		}
	}
	f.entryNode.read("/config/chains/"+clashTunnelChainName(stored.ID), &derived)
	if len(derived.Hops) != 1 || derived.Hops[0].Bypass != "" || derived.Hops[0].SockOpts.Mark != int(f.entryNode.mark) || derived.Hops[0].Nodes[0].SockOpts.Mark != int(f.entryNode.mark) {
		t.Fatalf("all-target route must have only the exemption mark: %+v", derived)
	}
	active, savedOnApply, _ := f.entryNode.redirectStatus()
	if !active || !savedOnApply {
		t.Fatal("original-target redirect did not follow the saved listener lifecycle")
	}
	base, marked := f.entryNode.chain(stored.ChainID), f.entryNode.chain(clashTunnelChainName(stored.ID))
	NewRuleSyncService(f.db).syncTunnels(utils.GetGostClient(&f.entry), f.entry.ID, []model.GostTunnel{*stored}, []gost.ChainConfig{base, marked},
		map[uint]*model.GostNode{f.exit.ID: {Address: "198.51.100.99"}})
	_, _, host := f.entryNode.redirectStatus()
	if host != "198.51.100.99" {
		t.Fatalf("all-target redirect host was not synchronized: %q", host)
	}
	for _, name := range []string{base.Name, marked.Name} {
		address, ok := f.entryNode.chain(name).TunnelRelayAddress()
		if !ok || address != "198.51.100.99:18443" {
			t.Fatalf("all-target relay route %s = %q", name, address)
		}
	}
	requireServiceError(t, f.stop(), nil)
	f.assertStopped(t)
	f.service = NewTunnelService(f.db)
	requireServiceError(t, f.start(), nil)
	requireServiceError(t, f.stop(), nil)
	f.assertStopped(t)
}

func TestTransparentIngressSelectedRulesAndDomainsUseDedicatedPolicy(t *testing.T) {
	f := newOriginalTargetTestTunnel(t)
	saveServiceFixtures(t, f.db,
		&model.GostRuleset{ID: 2, URL: "https://rules.example/one", Matchers: []string{".one.test"}},
		&model.GostRuleset{ID: 3, URL: "https://rules.example/two", Matchers: []string{".two.test"}})
	rules := NewRuleService(f.db)
	_, err := rules.Update(f.destination.ID, &dto.UpdateRuleReq{Name: "selected", Protocol: "tcp", ListenPort: f.destination.ListenPort,
		RulesetIDs: []uint{2, 3}, ManualDomains: []string{"Manual.Example."}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, f.start(), nil)
	policyName := transparentPolicyName(f.tunnel.ID)
	var bypass gost.AdmissionConfig
	f.entryNode.read("/config/bypasses/"+policyName, &bypass)
	for _, matcher := range []string{".one.test", ".two.test", ".manual.example:443"} {
		if !slices.Contains(bypass.Matchers, matcher) || !bypass.Whitelist {
			t.Fatalf("selected matchers did not reach the dedicated policy: %+v", bypass)
		}
	}
	var chain struct{ Hops []struct{ Bypass string } }
	f.entryNode.read("/config/chains/"+clashTunnelChainName(f.tunnel.ID), &chain)
	if chain.Hops[0].Bypass != policyName || f.entryNode.service(entranceServiceName(f.source.ID)).Handler.Metadata["sniffing"] != true ||
		f.entryNode.service(dockerEntranceServiceName(f.source.ID)).Handler.Metadata["sniffing"] != true {
		t.Fatal("selected rules require sniffing and a per-tunnel whitelist on both ingress listeners")
	}
	// Refreshing selected subscriptions updates only this tunnel's policy.
	updated := model.GostRuleset{ID: 2, Matchers: []string{".updated.test"}}
	requireServiceError(t, f.db.Model(&updated).Select("Matchers").Updates(&updated).Error, nil)
	NewGostRulesetService(f.db).sync(t.Context(), map[uint]bool{})
	f.entryNode.read("/config/bypasses/"+policyName, &bypass)
	if !slices.Contains(bypass.Matchers, ".updated.test") || slices.Contains(bypass.Matchers, ".one.test") {
		t.Fatalf("selected policy was not refreshed: %+v", bypass)
	}
	// A cleanup failure must remain retryable without discarding the binding.
	f.entryNode.mu.Lock()
	f.entryNode.reject = map[string]int{"DELETE /config/bypasses/" + policyName: http.StatusBadGateway}
	f.entryNode.mu.Unlock()
	if err := f.stop(); err == nil {
		t.Fatal("failed per-tunnel policy removal was ignored")
	}
	f.entryNode.mu.Lock()
	delete(f.entryNode.reject, "DELETE /config/bypasses/"+policyName)
	f.entryNode.mu.Unlock()
	requireServiceError(t, f.stop(), nil)
	f.assertStopped(t)
	var removed gost.AdmissionConfig
	f.entryNode.read("/config/bypasses/"+policyName, &removed)
	if removed.Name != "" {
		t.Fatal("stopped tunnel retained its private policy")
	}
	_, err = rules.Update(f.destination.ID, &dto.UpdateRuleReq{Name: "all targets", Protocol: "tcp", ListenPort: f.destination.ListenPort,
		RulesetIDs: []uint{}, ManualDomains: []string{}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, f.start(), nil)
	chain.Hops = nil
	f.entryNode.read("/config/chains/"+clashTunnelChainName(f.tunnel.ID), &chain)
	if chain.Hops[0].Bypass != "" || f.entryNode.service(entranceServiceName(f.source.ID)).Handler.Metadata["sniffing"] != nil {
		t.Fatal("cleared selections did not return to all-target forwarding")
	}
	requireServiceError(t, f.stop(), nil)
}

func TestTransparentTunnelRejectsMismatchedIngressAndTargetPolicy(t *testing.T) {
	for _, test := range []struct {
		name            string
		protocol        model.RuleProtocol
		original, clash bool
		targets         []string
		want            error
	}{
		{"original targets require red", "tcp", true, false, nil, bizerrors.ErrRuleOriginalTargetInvalid},
		{"transparent entry rejects fixed targets", "red", false, false, []string{"fixed.test:443"}, bizerrors.ErrRuleOriginalTargetInvalid},
		{"transparent entry needs target mode", "red", false, false, nil, bizerrors.ErrRuleTransparentTargetRequired},
		{"new and legacy target modes conflict", "red", true, true, nil, bizerrors.ErrEndpointTargets},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOriginalTargetTestTunnel(t)
			f.source.Protocol = test.protocol
			f.destination.UseOriginalTarget, f.destination.UseClashRuleset, f.destination.Targets = test.original, test.clash, test.targets
			requireServiceError(t, f.db.Model(&f.source).Select("Protocol").Updates(&f.source).Error, nil)
			requireServiceError(t, f.db.Model(&f.destination).Select("UseOriginalTarget", "UseClashRuleset", "Targets").Updates(&f.destination).Error, nil)
			requireServiceError(t, f.start(), test.want)
			if f.entryNode.service(fmt.Sprintf("rule-%d", f.source.ID)).Name != "" {
				t.Fatal("invalid target policy provisioned a listener")
			}
		})
	}
}

func TestLegacyEndpointTunnelStoresOriginalTargetPolicy(t *testing.T) {
	f := newTransparentTestTunnel(t)
	f.source.Protocol, f.source.UseClashRuleset = model.RuleProtocolTransparent, false
	requireServiceError(t, f.db.Model(&f.source).Select("Protocol", "UseClashRuleset").Updates(&f.source).Error, nil)
	requireServiceError(t, f.db.Delete(&f.tunnel).Error, nil)
	tunnel, err := f.service.Create(&dto.CreateTunnelReq{Name: "legacy selected endpoints", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID,
		UseOriginalTarget: true, ManualDomains: []string{"Selected.Example."}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	f.tunnel = *tunnel
	if !tunnel.UseOriginalTarget || !slices.Equal(tunnel.ManualDomains, []string{"selected.example"}) || tunnel.EntryRule.UseOriginalTarget {
		t.Fatalf("legacy endpoint tunnel did not own the normalized policy: %+v", tunnel)
	}
	requireServiceError(t, f.stop(), nil)
	_, err = f.service.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "rename", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err := f.service.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if !stored.UseOriginalTarget || !slices.Equal(stored.ManualDomains, []string{"selected.example"}) {
		t.Fatalf("omitted policy values were cleared: %+v", stored)
	}
	_, err = f.service.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "all original targets", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID,
		ManualDomains: []string{}, RulesetIDs: []uint{}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, f.start(), nil)
	if f.entryNode.service(entranceServiceName(f.source.ID)).Handler.Metadata["sniffing"] != nil {
		t.Fatal("cleared legacy endpoint selections did not disable filtering")
	}
	requireServiceError(t, f.stop(), nil)
}

func TestLegacyBoundRuleRejectsNewTransparentProtocol(t *testing.T) {
	db := newServiceTestDB(t)
	node := model.GostNode{Name: "entry", Status: model.NodeStatusOnline}
	saveServiceFixtures(t, db, &node)
	tunnel := model.GostTunnel{Name: "legacy", EntryNodeID: node.ID, Status: model.TunnelStatusRunning, ChainID: "existing"}
	saveServiceFixtures(t, db, &tunnel)
	rules := NewRuleService(db)
	_, err := rules.Create(&dto.CreateRuleReq{Name: "legacy red", Type: "tunnel", TunnelID: &tunnel.ID, Protocol: "red", ListenPort: 21300,
		UseOriginalTarget: true}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrEndpointProtocol)
	legacy := model.GostRule{Name: "stored unsupported red", Type: model.RuleTypeTunnel, TunnelID: &tunnel.ID,
		Protocol: model.RuleProtocolTransparent, ListenIP: "127.0.0.1", ListenPort: 21300, UseOriginalTarget: true}
	saveServiceFixtures(t, db, &legacy)
	requireServiceError(t, rules.Start(legacy.ID, testAdmin, testIP, testAgent), bizerrors.ErrEndpointProtocol)
}
