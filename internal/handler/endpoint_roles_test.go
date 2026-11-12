package handler

import (
	"fmt"
	"testing"

	"yagp/internal/model"
)

func TestEndpointHTTPRolesOwnTargetsAndTransport(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	node := model.GostNode{Name: "endpoints"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	create := func(role, protocol, policy string, port, status int) handlerReply {
		t.Helper()
		return f.request(t, "POST", "/rules", fmt.Sprintf(`{"name":"endpoint","node_id":%d,"role":%q,"protocol":%q,"listen_port":%d%s}`, node.ID, role, protocol, port, policy), status)
	}
	entry := handlerData[model.GostRule](t, create("entry", "tcp", "", 18000, 200))
	if entry.Role != model.EndpointRoleEntry || len(entry.Targets) != 0 || entry.UseClashRuleset {
		t.Fatalf("entry has a target policy: %+v", entry)
	}
	for i, protocol := range []string{"tcp", "udp", "tls", "mtls", "ws", "mws", "quic"} {
		t.Run(protocol, func(t *testing.T) {
			exit := handlerData[model.GostRule](t, create("exit", protocol, `,"targets":["origin.example:443","[2001:db8::1]:443"],"strategy":"hash"`, 18001+i, 200))
			stored := handlerData[model.GostRule](t, f.request(t, "GET", fmt.Sprintf("/rules/%d", exit.ID), "", 200))
			if stored.Role != model.EndpointRoleExit || string(stored.Protocol) != protocol || stored.Strategy != "hash" || len(stored.Targets) != 2 || stored.Targets[0] != "origin.example:443" {
				t.Fatalf("exit policy was not saved: %+v", stored)
			}
			// Role-specific endpoints are provisioned together by their tunnel.
			f.request(t, "POST", fmt.Sprintf("/rules/%d/start", exit.ID), "", 400)
		})
	}
	clash := handlerData[model.GostRule](t, create("exit", "quic", `,"use_clash_ruleset":true`, 18009, 200))
	if !clash.UseClashRuleset || clash.ListenIP != "" || clash.Protocol != "quic" {
		t.Fatalf("Clash policy incorrectly depends on the exit listener: %+v", clash)
	}
	for _, invalid := range []struct{ role, protocol, policy string }{
		{"entry", "tls", ""},
		{"entry", "tcp", `,"targets":["origin.example:443"]`},
		{"entry", "tcp", `,"use_clash_ruleset":true`},
		{"exit", "tcp", ""},
		{"exit", "tcp", `,"targets":["  "]`},
		{"exit", "tcp", `,"targets":["origin.example:443"],"use_clash_ruleset":true`},
		{"exit", "tls", `,"targets":["origin.example:443"],"listen_ip":"127.0.0.1"`},
		{"unknown", "tcp", ""},
		{"exit", "unknown", `,"use_clash_ruleset":true`},
	} {
		create(invalid.role, invalid.protocol, invalid.policy, 18010, 400)
	}
	// Different Gost transports can occupy the same underlying TCP/UDP socket.
	create("exit", "tls", `,"targets":["origin.example:443"]`, entry.ListenPort, 400)
	create("entry", "udp", "", clash.ListenPort, 400)
}

func TestEndpointHTTPUpdatesStoppedExitPolicyWithoutRebinding(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	node := model.GostNode{Name: "endpoints"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.GostRule{Name: "entry", NodeID: &node.ID, Role: model.EndpointRoleEntry, ListenPort: 18000}
	exit := model.GostRule{Name: "exit", NodeID: &node.ID, Role: model.EndpointRoleExit, Protocol: "tls", ListenPort: 18001, Targets: []string{"old.example:443"}}
	for _, rule := range []*model.GostRule{&entry, &exit} {
		if err := f.db.Create(rule).Error; err != nil {
			t.Fatal(err)
		}
	}
	tunnel := model.GostTunnel{Name: "tunnel", EntryRuleID: &entry.ID, ExitRuleID: &exit.ID}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/rules/%d", exit.ID)
	update := `{"name":"updated exit","protocol":"quic","listen_port":18001,"targets":["new.example:443"],"strategy":"fifo"}`
	stored := handlerData[model.GostRule](t, f.request(t, "PUT", path, update, 200))
	if stored.Role != model.EndpointRoleExit || stored.Protocol != "quic" || stored.Targets[0] != "new.example:443" || stored.LinkedTunnelStatus != model.TunnelStatusStopped {
		t.Fatalf("stopped exit policy update failed: %+v", stored)
	}
	page := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules", "", 200))
	for _, rule := range page.List {
		if rule.LinkedTunnelStatus != model.TunnelStatusStopped {
			t.Fatalf("list omitted linked tunnel state: %+v", rule)
		}
	}
	f.request(t, "PUT", path, `{"name":"moved exit","protocol":"quic","listen_port":18002}`, 400)
	f.request(t, "PUT", path, `{"name":"reversed exit","role":"entry","protocol":"tcp","listen_port":18001}`, 400)
	f.request(t, "PUT", fmt.Sprintf("/rules/%d", entry.ID), `{"name":"entry","protocol":"tcp","listen_port":18000}`, 400)
	if err := f.db.Model(&tunnel).Update("status", model.TunnelStatusRunning).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "PUT", path, update, 400)
	stored = handlerData[model.GostRule](t, f.request(t, "GET", path, "", 200))
	if stored.Role != model.EndpointRoleExit || stored.ListenPort != 18001 || stored.Protocol != "quic" || stored.Targets[0] != "new.example:443" {
		t.Fatalf("rejected update changed the exit: %+v", stored)
	}
}
