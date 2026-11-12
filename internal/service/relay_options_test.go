package service

import (
	"maps"
	"testing"

	"yagp/internal/dto"
	"yagp/internal/model"
)

func TestExitProtocolOptionsReachBothNodesAfterUpdateAndRestart(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	rules := NewRuleService(db)
	source, err := rules.Create(&dto.CreateRuleReq{Name: "source", NodeID: &entry.ID, Role: "entry", Protocol: "tcp", ListenPort: 18001}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	options := map[string]string{"path": "/first", "tls_server_name": "first.example"}
	destination, err := rules.Create(&dto.CreateRuleReq{Name: "destination", NodeID: &exit.ID, Role: "exit", Protocol: "wss", ProtocolOptions: options,
		ListenPort: 18002, Targets: []string{"origin.test:443"}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	tunnels := NewTunnelService(db)
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{Name: "options", EntryRuleID: source.ID, ExitRuleID: destination.ID,
		Protocol: "tcp", ProtocolOptions: map[string]string{"obsolete_tunnel_option": "ignored"}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	assertOptions := func(wantProtocol string, want map[string]string) {
		t.Helper()
		stored, err := tunnels.GetByID(tunnel.ID)
		requireServiceError(t, err, nil)
		if len(stored.ProtocolOptions) != 0 || !maps.Equal(stored.ExitRule.ProtocolOptions, want) || len(stored.EntryRule.ProtocolOptions) != 0 {
			t.Fatalf("exit options did not remain authoritative: tunnel=%v exit=%v entry=%v", stored.ProtocolOptions, stored.ExitRule.ProtocolOptions, stored.EntryRule.ProtocolOptions)
		}
		relay := exitRemote.service(stored.ServiceID)
		dialer := entryRemote.chain(stored.ChainID).Hops[0].Nodes[0].Dialer
		if relay.Listener.Type != wantProtocol || dialer.Type != wantProtocol {
			t.Fatalf("transport = %s/%s, want %s", relay.Listener.Type, dialer.Type, wantProtocol)
		}
		if want["path"] != "" && (relay.Listener.Metadata["ws.path"] != want["path"] || dialer.Metadata["ws.path"] != want["path"]) {
			t.Fatalf("path did not reach both sides: listener=%v dialer=%v", relay.Listener.Metadata, dialer.Metadata)
		}
		if want["max_idle_timeout"] != "" && (relay.Listener.Metadata["maxIdleTimeout"] != want["max_idle_timeout"] || dialer.Metadata["maxIdleTimeout"] != want["max_idle_timeout"]) {
			t.Fatalf("QUIC timeout did not reach both sides: listener=%v dialer=%v", relay.Listener.Metadata, dialer.Metadata)
		}
		if want["tls_server_name"] != "" && dialer.TLS["serverName"] != want["tls_server_name"] {
			t.Fatalf("server name did not reach dialer: %+v", dialer.TLS)
		}
		if len(want) == 0 && (len(relay.Listener.Metadata) != 0 || len(dialer.Metadata) != 0 || len(dialer.TLS) != 0) {
			t.Fatalf("changed protocol retained stale options: listener=%+v dialer=%+v", relay.Listener, dialer)
		}
	}
	assertOptions("wss", options)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	// A prior tunnel snapshot must not retain obsolete exit settings after restart.
	tunnel.ProtocolOptions = map[string]string{"path": "/obsolete", "tls_client_key": "/old/client.key"}
	requireServiceError(t, db.Model(tunnel).Select("ProtocolOptions").Updates(tunnel).Error, nil)
	options = map[string]string{"path": "/changed", "tls_server_name": "changed.example"}
	_, err = rules.Update(destination.ID, &dto.UpdateRuleReq{Name: "edited bound exit", Protocol: "wss", ListenPort: destination.ListenPort, ProtocolOptions: options}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	tunnels = NewTunnelService(db)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	assertOptions("wss", options)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	_, err = rules.Update(destination.ID, &dto.UpdateRuleReq{Name: "new protocol", Protocol: "tcp", ListenPort: destination.ListenPort}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	assertOptions("tcp", nil)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	replacementOptions := map[string]string{"path": "/replacement"}
	replacement, err := rules.Create(&dto.CreateRuleReq{Name: "replacement", NodeID: &exit.ID, Role: "exit", Protocol: "ws", ProtocolOptions: replacementOptions,
		ListenPort: 18003, Targets: []string{"replacement.test:443"}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	_, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "replacement", EntryRuleID: source.ID, ExitRuleID: replacement.ID,
		ProtocolOptions: map[string]string{"obsolete_tunnel_option": "ignored"}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	assertOptions("ws", replacementOptions)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	quicOptions := map[string]string{"max_idle_timeout": "37s"}
	_, err = rules.Update(replacement.ID, &dto.UpdateRuleReq{Name: "QUIC exit", Protocol: "quic", ListenPort: replacement.ListenPort, ProtocolOptions: quicOptions}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	assertOptions("quic", quicOptions)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
}

func TestLegacyTunnelProtocolOptionsReachBothNodes(t *testing.T) {
	for _, endpointBased := range []bool{false, true} {
		name := "node-based"
		if endpointBased {
			name = "legacy-endpoints"
		}
		t.Run(name, func(t *testing.T) {
			db := newServiceTestDB(t)
			loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
			entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
			entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
			exit.Name = "exit"
			saveServiceFixtures(t, db, &entry, &exit)
			options := map[string]string{"path": "/legacy", "tls_server_name": "legacy.example"}
			req := &dto.CreateTunnelReq{Name: "legacy options", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "wss", RelayPort: 18002, ProtocolOptions: options}
			if endpointBased {
				source := model.GostRule{Name: "source", NodeID: &entry.ID, Protocol: "tcp", ListenPort: 18001, Targets: []string{"origin.test:443"}}
				destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Protocol: "udp", ListenPort: 18002}
				saveServiceFixtures(t, db, &source, &destination)
				req.EntryNodeID, req.ExitNodeID, req.RelayPort = 0, 0, 0
				req.EntryRuleID, req.ExitRuleID = source.ID, destination.ID
			}
			tunnels := NewTunnelService(db)
			tunnel, err := tunnels.Create(req, testAdmin, testIP, testAgent)
			requireServiceError(t, err, nil)
			if endpointBased {
				requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
			}
			tunnels = NewTunnelService(db)
			requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
			stored, err := tunnels.GetByID(tunnel.ID)
			requireServiceError(t, err, nil)
			relay := exitRemote.service(stored.ServiceID)
			dialer := entryRemote.chain(stored.ChainID).Hops[0].Nodes[0].Dialer
			if !maps.Equal(stored.ProtocolOptions, options) || relay.Listener.Metadata["ws.path"] != "/legacy" || dialer.Metadata["ws.path"] != "/legacy" || dialer.TLS["serverName"] != "legacy.example" {
				t.Fatalf("legacy options were lost: saved=%v listener=%+v dialer=%+v", stored.ProtocolOptions, relay.Listener, dialer)
			}
			requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
		})
	}
}
