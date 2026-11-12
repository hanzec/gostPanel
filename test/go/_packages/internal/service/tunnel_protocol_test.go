package service

import (
	"testing"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/internal/repository"
)

func TestEndpointTunnelUsesSelectedTransport(t *testing.T) {
	for _, protocol := range []string{"", "tcp", "udp", "tls", "mtls", "ws", "mws", "quic"} {
		name, want := protocol, protocol
		if protocol == "" {
			name, want = "older client", "udp"
		}
		t.Run(name, func(t *testing.T) {
			db := newServiceTestDB(t)
			loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
			entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
			entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
			exit.Name = "exit"
			saveServiceFixtures(t, db, &entry, &exit)
			source := model.GostRule{Name: "source", NodeID: &entry.ID, Protocol: model.RuleProtocolTCP, ListenPort: 18001}
			destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Protocol: model.RuleProtocolUDP, ListenPort: 18002}
			saveServiceFixtures(t, db, &source, &destination)
			tunnels := NewTunnelService(db)
			tunnel, err := tunnels.Create(&dto.CreateTunnelReq{
				Name: "selected transport", EntryRuleID: source.ID, ExitRuleID: destination.ID,
				Protocol: protocol, Targets: []string{"origin.test:443"}, Strategy: "round",
			}, testAdmin, testIP, testAgent)
			requireServiceError(t, err, nil)
			assertTransport := func() {
				t.Helper()
				stored, err := tunnels.GetByID(tunnel.ID)
				requireServiceError(t, err, nil)
				relay := exitRemote.service(stored.ServiceID)
				chain := entryRemote.chain(stored.ChainID)
				if stored.Protocol != want || relay.Listener.Type != want || chain.Hops[0].Nodes[0].Dialer.Type != want {
					t.Fatalf("transport mismatch: saved=%q relay=%q dialer=%q, want %q", stored.Protocol, relay.Listener.Type, chain.Hops[0].Nodes[0].Dialer.Type, want)
				}
				if relay.Addr != ":18002" || entryRemote.service(stored.EntryRule.ServiceID).Listener.Type != "tcp" {
					t.Fatal("tunnel transport changed the source listener or destination port")
				}
				if stored.EntryRule.Protocol != model.RuleProtocolTCP || stored.ExitRule.Protocol != model.RuleProtocolUDP {
					t.Fatal("tunnel transport changed the reusable endpoint protocols")
				}
			}
			assertTransport()
			requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
			tunnels = NewTunnelService(db)
			requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
			assertTransport()
			requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
		})
	}
}

func TestEndpointTunnelUpdatesTransportIndependentlyOfEndpoints(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	source := model.GostRule{Name: "source", NodeID: &entry.ID, Protocol: model.RuleProtocolTCP, ListenPort: 18001}
	destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Protocol: model.RuleProtocolUDP, ListenPort: 18002}
	replacement := model.GostRule{Name: "replacement", NodeID: &exit.ID, Protocol: model.RuleProtocolTCP, ListenPort: 18003}
	saveServiceFixtures(t, db, &source, &destination, &replacement)
	tunnels := NewTunnelService(db)
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{
		Name: "selected transport", EntryRuleID: source.ID, ExitRuleID: destination.ID,
		Protocol: "tls", Targets: []string{"origin.test:443"}, Strategy: "round",
	}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	request := &dto.UpdateTunnelReq{Name: "new destination", EntryRuleID: source.ID, ExitRuleID: replacement.ID}
	_, err = tunnels.Update(tunnel.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if stored.Protocol != "tls" || stored.RelayPort != replacement.ListenPort {
		t.Fatalf("omitting transport while replacing an endpoint lost the saved transport: %+v", stored)
	}
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if exitRemote.service(relayServiceName(tunnel.ID)).Listener.Type != "tls" ||
		entryRemote.chain(tunnelChainName(tunnel.ID)).Hops[0].Nodes[0].Dialer.Type != "tls" {
		t.Fatal("restarting with a new endpoint did not retain the saved transport")
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	request.Protocol = "quic"
	_, err = tunnels.Update(tunnel.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	tunnels = NewTunnelService(db)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	stored, err = tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	relay := exitRemote.service(stored.ServiceID)
	chain := entryRemote.chain(stored.ChainID)
	if stored.Protocol != "quic" || relay.Listener.Type != "quic" || chain.Hops[0].Nodes[0].Dialer.Type != "quic" || relay.Addr != ":18003" {
		t.Fatal("updated transport was not persisted and applied to both tunnel endpoints")
	}
	rules := repository.NewRuleRepository(db)
	for _, rule := range []*model.GostRule{&source, &destination, &replacement} {
		storedRule, err := rules.FindByID(rule.ID)
		requireServiceError(t, err, nil)
		if storedRule.Protocol != rule.Protocol {
			t.Fatalf("updating tunnel transport changed endpoint %d protocol", rule.ID)
		}
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
}
