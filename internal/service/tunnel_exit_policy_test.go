package service

import (
	"slices"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
)

func TestEndpointTunnelUsesExitOwnedPolicyAndTransport(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	source := model.GostRule{Name: "source", NodeID: &entry.ID, Role: model.EndpointRoleEntry,
		Protocol: model.RuleProtocolTCP, ListenPort: 18001}
	destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Role: model.EndpointRoleExit,
		Protocol: "tls", ListenPort: 18002, Targets: []string{"one.test:443", "two.test:443"}, Strategy: "fifo"}
	replacement := model.GostRule{Name: "replacement", NodeID: &exit.ID, Role: model.EndpointRoleExit,
		Protocol: "mws", ListenPort: 18003, Targets: []string{"three.test:443", "four.test:443"}, Strategy: "rand"}
	saveServiceFixtures(t, db, &source, &destination, &replacement)
	tunnels := NewTunnelService(db)
	// Older clients may still send tunnel targets and transport; those must not replace the exit policy.
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{Name: "exit policy", EntryRuleID: source.ID, ExitRuleID: destination.ID,
		Protocol: "udp", Targets: []string{"obsolete.test:80"}, UseClashRuleset: true, Strategy: "hash"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	assertPolicy := func(want *model.GostRule) {
		t.Helper()
		stored, err := tunnels.GetByID(tunnel.ID)
		requireServiceError(t, err, nil)
		forward := entryRemote.service(stored.EntryRule.ServiceID)
		relay := exitRemote.service(stored.ServiceID)
		chain := entryRemote.chain(stored.ChainID)
		if stored.Protocol != string(want.Protocol) || stored.RelayPort != want.ListenPort ||
			relay.Listener.Type != string(want.Protocol) || chain.Hops[0].Nodes[0].Dialer.Type != string(want.Protocol) {
			t.Fatalf("exit transport was not used: saved=%q relay=%q dialer=%q", stored.Protocol, relay.Listener.Type, chain.Hops[0].Nodes[0].Dialer.Type)
		}
		if len(stored.Targets) != 0 || stored.UseClashRuleset || stored.Strategy != "" || len(stored.EntryRule.Targets) != 0 {
			t.Fatal("target policy was copied to the tunnel or source endpoint")
		}
		if !slices.Equal(stored.ExitRule.Targets, want.Targets) || stored.ExitRule.Strategy != want.Strategy ||
			forward.Forwarder == nil || len(forward.Forwarder.Nodes) != len(want.Targets) || forward.Forwarder.Selector.Strategy != want.Strategy {
			t.Fatalf("exit target policy was not preserved and applied: %+v", forward.Forwarder)
		}
		for i, target := range want.Targets {
			if forward.Forwarder.Nodes[i].Addr != target {
				t.Fatalf("forwarder target %d = %q, want %q", i, forward.Forwarder.Nodes[i].Addr, target)
			}
		}
	}
	assertPolicy(&destination)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	destination.Protocol, destination.ListenPort, destination.Strategy = "quic", 18004, "hash"
	destination.Targets = []string{"updated-one.test:8443", "updated-two.test:8443"}
	requireServiceError(t, db.Model(&destination).Select("Protocol", "ListenPort", "Targets", "Strategy").Updates(&destination).Error, nil)
	tunnels = NewTunnelService(db)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	assertPolicy(&destination)
	if exitRemote.service(relayServiceName(tunnel.ID)).Addr != ":18004" {
		t.Fatal("restart did not use the current exit listener port")
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	_, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "replacement", EntryRuleID: source.ID, ExitRuleID: replacement.ID,
		Protocol: "tcp", Targets: []string{"obsolete.test:80"}, Strategy: "hash"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	assertPolicy(&replacement)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
}

func TestEndpointTunnelRejectsRolesInTheWrongDirection(t *testing.T) {
	db := newServiceTestDB(t)
	first, second := model.GostNode{Name: "first"}, model.GostNode{Name: "second"}
	saveServiceFixtures(t, db, &first, &second)
	source := model.GostRule{Name: "source", NodeID: &first.ID, Role: model.EndpointRoleEntry, ListenPort: 18001}
	destination := model.GostRule{Name: "exit", NodeID: &second.ID, Role: model.EndpointRoleExit, ListenPort: 18002,
		Targets: []string{"origin.test:443"}}
	wrongDestination := model.GostRule{Name: "another entry", NodeID: &second.ID, Role: model.EndpointRoleEntry, ListenPort: 18003}
	saveServiceFixtures(t, db, &source, &destination, &wrongDestination)
	tunnels := NewTunnelService(db)
	for _, test := range []struct {
		entry, exit uint
		want        error
	}{
		{destination.ID, source.ID, bizerrors.ErrTunnelEntryInvalid},
		{source.ID, wrongDestination.ID, bizerrors.ErrTunnelExitInvalid},
	} {
		_, err := tunnels.Create(&dto.CreateTunnelReq{Name: "wrong role", EntryRuleID: test.entry, ExitRuleID: test.exit}, testAdmin, testIP, testAgent)
		requireServiceError(t, err, test.want)
	}
}

func TestExitClashPolicyValidatesIngressAndDrivesRulesetSync(t *testing.T) {
	f := newTransparentTestTunnel(t)
	requireServiceError(t, f.db.Model(&f.source).Updates(map[string]any{"role": model.EndpointRoleEntry, "use_clash_ruleset": false}).Error, nil)
	requireServiceError(t, f.db.Model(&f.destination).Updates(map[string]any{"role": model.EndpointRoleExit, "protocol": "tls", "use_clash_ruleset": true}).Error, nil)
	for _, invalid := range []map[string]any{
		{"protocol": "udp", "listen_ip": "127.0.0.1"},
		{"protocol": "tcp", "listen_ip": "0.0.0.0"},
	} {
		requireServiceError(t, f.db.Model(&f.source).Updates(invalid).Error, nil)
		requireServiceError(t, f.start(), bizerrors.ErrRuleClashInvalid)
	}
	requireServiceError(t, f.db.Model(&f.source).Updates(map[string]any{"protocol": "tcp", "listen_ip": "127.0.0.1"}).Error, nil)
	requireServiceError(t, f.start(), nil)
	stored, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	forward := f.entryNode.service(stored.EntryRule.ServiceID)
	if stored.EntryRule.UseClashRuleset || stored.UseClashRuleset || !stored.ExitRule.UseClashRuleset ||
		forward.Listener.Type != "red" || forward.Handler.Chain != clashTunnelChainName(stored.ID) ||
		f.exitNode.service(stored.ServiceID).Listener.Type != "tls" {
		t.Fatal("Clash forwarding did not use the selected exit policy and transport")
	}
	setMatchers := func(match string) {
		t.Helper()
		ruleset := model.GostRuleset{ID: gostRulesetID, Matchers: []string{match}}
		requireServiceError(t, f.db.Model(&ruleset).Select("Matchers").Updates(&ruleset).Error, nil)
	}
	subscriptions := NewGostRulesetService(f.db)
	setMatchers(".exit-policy.test")
	subscriptions.sync(t.Context(), map[uint]bool{})
	var bypass gost.AdmissionConfig
	f.entryNode.read("/config/bypasses/"+gost.TransparentBypassName, &bypass)
	if !slices.Equal(bypass.Matchers, []string{".exit-policy.test"}) {
		t.Fatalf("ruleset did not reach the entry of an exit-owned Clash tunnel: %+v", bypass)
	}
	requireServiceError(t, f.stop(), nil)
	f.destination.Targets, f.destination.UseClashRuleset = []string{"fixed.test:443"}, false
	requireServiceError(t, f.db.Model(&f.destination).Select("Targets", "UseClashRuleset").Updates(&f.destination).Error, nil)
	// Obsolete source and tunnel flags must not re-enable Clash after the exit switches to fixed targets.
	requireServiceError(t, f.db.Model(&f.source).Update("use_clash_ruleset", true).Error, nil)
	requireServiceError(t, f.db.Model(&f.tunnel).Updates(map[string]any{"use_clash_ruleset": true, "strategy": "round"}).Error, nil)
	requireServiceError(t, f.start(), nil)
	forward = f.entryNode.service(entranceServiceName(f.source.ID))
	if forward.Forwarder == nil || forward.Forwarder.Nodes[0].Addr != "fixed.test:443" {
		t.Fatal("fixed exit target did not override obsolete Clash flags")
	}
	setMatchers(".must-not-sync.test")
	subscriptions.sync(t.Context(), map[uint]bool{})
	f.entryNode.read("/config/bypasses/"+gost.TransparentBypassName, &bypass)
	if !slices.Equal(bypass.Matchers, []string{".exit-policy.test"}) {
		t.Fatal("obsolete flags caused fixed-target tunnel ruleset synchronization")
	}
	requireServiceError(t, f.stop(), nil)
}
