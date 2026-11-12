package handler

import (
	"fmt"
	"testing"

	"yagp/internal/model"
)

func TestEntranceHTTPInventoriesAndProtectsTunnelEndpoints(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	nodeA := model.GostNode{Name: "node-a", Port: 39110}
	nodeB := model.GostNode{Name: "node-b", Port: 39111}
	for _, node := range []*model.GostNode{&nodeA, &nodeB} {
		if err := f.db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	aBody := fmt.Sprintf(`{"name":"A","node_id":%d,"protocol":"tcp","listen_ip":"127.0.0.1","listen_port":9443,"use_clash_ruleset":true}`, nodeA.ID)
	bBody := fmt.Sprintf(`{"name":"B","node_id":%d,"protocol":"tcp","listen_ip":"192.0.2.4","listen_port":9443}`, nodeB.ID)
	a := handlerData[model.GostRule](t, f.request(t, "POST", "/rules", aBody, 200))
	b := handlerData[model.GostRule](t, f.request(t, "POST", "/rules", bBody, 200))
	if a.Type != model.RuleTypeForward || a.TunnelID != nil || a.NodeID == nil || *a.NodeID != nodeA.ID || !a.UseClashRuleset || len(a.Targets) != 0 {
		t.Fatalf("unbound Clash entrance = %+v", a)
	}
	if b.Type != model.RuleTypeForward || b.TunnelID != nil || b.NodeID == nil || *b.NodeID != nodeB.ID || b.ListenPort != 9443 || b.UseClashRuleset || len(b.Targets) != 0 {
		t.Fatalf("unbound relay entrance = %+v", b)
	}
	aPath, bPath := fmt.Sprintf("/rules/%d", a.ID), fmt.Sprintf("/rules/%d", b.ID)
	for _, path := range []string{aPath, bPath} {
		if reply := f.request(t, "POST", path+"/start", "", 400); reply.Code != 10114 {
			t.Fatalf("standalone start of %s = %+v", path, reply)
		}
	}

	moveB := fmt.Sprintf(`{"name":"B","node_id":%d,"protocol":"tcp","listen_port":8443}`, nodeA.ID)
	moved := handlerData[model.GostRule](t, f.request(t, "PUT", bPath, moveB, 200))
	if moved.NodeID == nil || *moved.NodeID != nodeA.ID || moved.ListenIP != b.ListenIP {
		t.Fatalf("moving entrance discarded its listener settings: %+v", moved)
	}
	f.request(t, "PUT", bPath, bBody, 200)

	tunnel := model.GostTunnel{
		Name: "A to B", EntryNodeID: nodeA.ID, ExitNodeID: nodeB.ID,
		EntryRuleID: &a.ID, ExitRuleID: &b.ID, Protocol: "tcp", RelayPort: 9443,
	}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	assertBinding := func(rule model.GostRule, want string) {
		t.Helper()
		if rule.LinkedTunnelID == nil || *rule.LinkedTunnelID != tunnel.ID || rule.TunnelRole != want {
			t.Fatalf("%s entrance binding = %+v", want, rule)
		}
	}
	assertBinding(handlerData[model.GostRule](t, f.request(t, "GET", aPath, "", 200)), "entry")
	assertBinding(handlerData[model.GostRule](t, f.request(t, "GET", bPath, "", 200)), "exit")
	page := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules", "", 200))
	if len(page.List) != 2 {
		t.Fatalf("entrance list = %+v", page.List)
	}
	roles := map[uint]string{a.ID: "entry", b.ID: "exit"}
	for _, rule := range page.List {
		assertBinding(rule, roles[rule.ID])
	}
	for _, operation := range []struct{ method, path, body string }{
		{"PUT", bPath, moveB}, {"DELETE", bPath, ""}, {"POST", aPath + "/start", ""}, {"POST", aPath + "/stop", ""},
	} {
		if reply := f.request(t, operation.method, operation.path, operation.body, 400); reply.Code != 10113 {
			t.Fatalf("changing linked entrance via %s %s = %+v", operation.method, operation.path, reply)
		}
	}
}

func TestEndpointTunnelHTTPAcceptsAndValidatesSelectedTransport(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	entryNode, exitNode := model.GostNode{Name: "entry"}, model.GostNode{Name: "exit"}
	for _, node := range []*model.GostNode{&entryNode, &exitNode} {
		if err := f.db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := model.GostRule{Name: "source", NodeID: &entryNode.ID, ListenPort: 18001}
	destination := model.GostRule{Name: "destination", NodeID: &exitNode.ID, ListenPort: 18002}
	for _, rule := range []*model.GostRule{&source, &destination} {
		if err := f.db.Create(rule).Error; err != nil {
			t.Fatal(err)
		}
	}
	tunnel := model.GostTunnel{Name: "tunnel", EntryNodeID: entryNode.ID, ExitNodeID: exitNode.ID,
		EntryRuleID: &source.ID, ExitRuleID: &destination.ID, Protocol: "tcp", RelayPort: destination.ListenPort,
		Targets: []string{"origin.test:443"}, Strategy: "round"}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/tunnels/%d", tunnel.ID)
	body := fmt.Sprintf(`{"name":"selected TLS","entry_rule_id":%d,"exit_rule_id":%d,"protocol":"tls"}`, source.ID, destination.ID)
	updated := handlerData[model.GostTunnel](t, f.request(t, "PUT", path, body, 200))
	if updated.Protocol != "tls" || updated.RelayPort != destination.ListenPort {
		t.Fatalf("selected tunnel transport was not accepted: %+v", updated)
	}
	invalid := fmt.Sprintf(`{"name":"invalid","entry_rule_id":%d,"exit_rule_id":%d,"protocol":"unsupported"}`, source.ID, destination.ID)
	f.request(t, "POST", "/tunnels", invalid, 400)
	f.request(t, "PUT", path, invalid, 400)
	stored := handlerData[model.GostTunnel](t, f.request(t, "GET", path, "", 200))
	if stored.Name != "selected TLS" || stored.Protocol != "tls" {
		t.Fatalf("invalid transport changed the saved tunnel: %+v", stored)
	}
}
