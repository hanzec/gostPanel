package handler

import (
	"fmt"
	"maps"
	"testing"

	"yagp/internal/model"
)

func TestExitProtocolOptionsHTTPPersistenceAndValidation(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	node := model.GostNode{Name: "options"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	create := func(role, protocol, options string, status int) handlerReply {
		t.Helper()
		return f.request(t, "POST", "/rules", fmt.Sprintf(`{"name":"options","node_id":%d,"role":%q,"protocol":%q,"listen_port":18001,"targets":["origin.test:443"],"protocol_options":%s}`, node.ID, role, protocol, options), status)
	}
	options := map[string]string{"path": "/relay", "tls_server_name": "relay.example"}
	exit := handlerData[model.GostRule](t, create("exit", "wss", `{"path":"/relay","tls_server_name":"relay.example"}`, 200))
	path := fmt.Sprintf("/rules/%d", exit.ID)
	assertSaved := func(want map[string]string) {
		t.Helper()
		stored := handlerData[model.GostRule](t, f.request(t, "GET", path, "", 200))
		if !maps.Equal(stored.ProtocolOptions, want) {
			t.Fatalf("stored options = %v, want %v", stored.ProtocolOptions, want)
		}
		page := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules", "", 200))
		if len(page.List) != 1 || !maps.Equal(page.List[0].ProtocolOptions, want) {
			t.Fatalf("list omitted options: %+v", page.List)
		}
	}
	assertSaved(options)
	f.request(t, "PUT", path, `{"name":"omitted","protocol":"wss","listen_port":18001}`, 200)
	assertSaved(options)
	f.request(t, "PUT", path, `{"name":"clear","protocol":"wss","listen_port":18001,"protocol_options":{}}`, 200)
	assertSaved(nil)
	f.request(t, "PUT", path, `{"name":"replace","protocol":"wss","listen_port":18001,"protocol_options":{"path":"/new"}}`, 200)
	assertSaved(map[string]string{"path": "/new"})
	f.request(t, "PUT", path, `{"name":"changed transport","protocol":"tls","listen_port":18001}`, 200)
	assertSaved(nil)
	for _, invalid := range []string{
		`{"path":"/not-tls"}`, `{"unknown_option":"x"}`, `{"tls_server_name":17}`, `[]`,
	} {
		f.request(t, "PUT", path, fmt.Sprintf(`{"name":"invalid","protocol":"tls","listen_port":18001,"protocol_options":%s}`, invalid), 400)
		assertSaved(nil)
	}
	create("", "tcp", `{"path":"/legacy-rule"}`, 400)
	f.request(t, "POST", "/rules", fmt.Sprintf(`{"name":"entry","node_id":%d,"role":"entry","protocol":"tcp","listen_port":18002,"protocol_options":{"path":"/entry"}}`, node.ID), 400)
}

func TestLegacyTunnelProtocolOptionsHTTPPersistence(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	entry, exit := model.GostNode{Name: "entry"}, model.GostNode{Name: "exit"}
	for _, node := range []*model.GostNode{&entry, &exit} {
		if err := f.db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"name":"legacy","entry_node_id":%d,"exit_node_id":%d,"protocol":"ws","relay_port":8443,"protocol_options":{"path":"/legacy"}}`, entry.ID, exit.ID)
	tunnel := handlerData[model.GostTunnel](t, f.request(t, "POST", "/tunnels", body, 200))
	path := fmt.Sprintf("/tunnels/%d", tunnel.ID)
	assertSaved := func(want map[string]string) {
		t.Helper()
		stored := handlerData[model.GostTunnel](t, f.request(t, "GET", path, "", 200))
		if !maps.Equal(stored.ProtocolOptions, want) {
			t.Fatalf("stored tunnel options = %v, want %v", stored.ProtocolOptions, want)
		}
	}
	assertSaved(map[string]string{"path": "/legacy"})
	f.request(t, "PUT", path, `{"name":"omitted","protocol":"ws","relay_port":8443}`, 200)
	assertSaved(map[string]string{"path": "/legacy"})
	f.request(t, "PUT", path, `{"name":"invalid","protocol":"tcp","relay_port":8443,"protocol_options":{"path":"/bad"}}`, 400)
	assertSaved(map[string]string{"path": "/legacy"})
	f.request(t, "PUT", path, `{"name":"clear","protocol":"ws","relay_port":8443,"protocol_options":{}}`, 200)
	assertSaved(nil)
	f.request(t, "PUT", path, `{"name":"replace","protocol":"ws","relay_port":8443,"protocol_options":{"path":"/restored"}}`, 200)
	f.request(t, "PUT", path, `{"name":"new transport","protocol":"tls","relay_port":8443}`, 200)
	assertSaved(nil)
}
