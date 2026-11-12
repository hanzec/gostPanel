package handler

import (
	"fmt"
	"slices"
	"testing"

	"yagp/internal/model"
)

func TestTransparentIngressHTTPDefaultsAndRejectsInvalidPolicy(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	node := model.GostNode{Name: "transparent"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	create := func(protocol, role, extra string, port, status int) handlerReply {
		t.Helper()
		return f.request(t, "POST", "/rules", fmt.Sprintf(`{"name":"endpoint","node_id":%d,"role":%q,"protocol":%q,"listen_port":%d%s}`, node.ID, role, protocol, port, extra), status)
	}
	entry := handlerData[model.GostRule](t, create("red", "entry", "", 21000, 200))
	if entry.Protocol != model.RuleProtocolTransparent || entry.ListenIP != "127.0.0.1" || entry.UseOriginalTarget {
		t.Fatalf("transparent listener was not stored as an independent entry: %+v", entry)
	}
	path := fmt.Sprintf("/rules/%d", entry.ID)
	stored := handlerData[model.GostRule](t, f.request(t, "GET", path, "", 200))
	if stored.Protocol != model.RuleProtocolTransparent || stored.ListenIP != "127.0.0.1" {
		t.Fatalf("transparent entry was not persisted: %+v", stored)
	}
	f.request(t, "POST", path+"/start", "", 400)
	for _, address := range []string{"0.0.0.0", "192.0.2.1", "::", "not-an-ip"} {
		create("red", "entry", fmt.Sprintf(`,"listen_ip":%q`, address), 21001, 400)
	}
	loopback := handlerData[model.GostRule](t, create("red", "entry", `,"listen_ip":"::1"`, 21001, 200))
	if loopback.ListenIP != "127.0.0.1" {
		t.Fatalf("loopback was not normalized: %q", loopback.ListenIP)
	}
	create("tcp", "entry", `,"listen_ip":"127.0.0.1"`, 21000, 400)
	create("udp", "entry", `,"listen_ip":"127.0.0.1"`, 21000, 200)
	for _, extra := range []string{`,"targets":["origin.test:443"]`, `,"use_original_target":true`, `,"manual_domains":["example.test"]`} {
		create("red", "entry", extra, 21002, 400)
	}
	create("red", "exit", `,"use_original_target":true`, 21003, 400)
	create("tls", "exit", `,"use_original_target":true,"targets":["origin.test:443"]`, 21003, 400)
	create("tls", "exit", `,"use_original_target":true,"use_clash_ruleset":true`, 21003, 400)
	create("tls", "exit", `,"targets":["origin.test:443"],"manual_domains":["example.test"]`, 21003, 400)
	exit := handlerData[model.GostRule](t, create("tls", "exit", `,"use_original_target":true`, 21003, 200))
	if !exit.UseOriginalTarget || exit.UseClashRuleset || len(exit.Targets) != 0 {
		t.Fatalf("original-target exit was not saved: %+v", exit)
	}
	// Changing an unbound ordinary listener to red supplies the managed loopback automatically.
	ordinary := handlerData[model.GostRule](t, create("tcp", "entry", `,"listen_ip":"192.0.2.1"`, 21004, 200))
	changed := handlerData[model.GostRule](t, f.request(t, "PUT", fmt.Sprintf("/rules/%d", ordinary.ID), `{"name":"converted","protocol":"red","listen_port":21004}`, 200))
	if changed.ListenIP != "127.0.0.1" || changed.Protocol != model.RuleProtocolTransparent {
		t.Fatalf("transparent conversion kept the old bind: %+v", changed)
	}
}

func TestOriginalTargetSelectionsHTTPPreserveAndClear(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	node := model.GostNode{Name: "exit"}
	ruleset := model.GostRuleset{ID: 2, URL: "https://rules.example/list", Matchers: []string{".rules.example"}}
	for _, value := range []any{&node, &ruleset} {
		if err := f.db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"name":"filtered exit","node_id":%d,"role":"exit","protocol":"tls","listen_port":21100,"use_original_target":true,"ruleset_ids":[2,2],"manual_domains":["Example.Test.","example.test"]}`, node.ID)
	exit := handlerData[model.GostRule](t, f.request(t, "POST", "/rules", body, 200))
	path := fmt.Sprintf("/rules/%d", exit.ID)
	assertPolicy := func(ids []uint, domains []string, original bool) {
		t.Helper()
		stored := handlerData[model.GostRule](t, f.request(t, "GET", path, "", 200))
		if !slices.Equal(stored.RulesetIDs, ids) || !slices.Equal(stored.ManualDomains, domains) || stored.UseOriginalTarget != original {
			t.Fatalf("selection was not persisted: %+v", stored)
		}
	}
	assertPolicy([]uint{2}, []string{"example.test"}, true)
	f.request(t, "PUT", path, `{"name":"renamed","protocol":"tls","listen_port":21100}`, 200)
	assertPolicy([]uint{2}, []string{"example.test"}, true)
	f.request(t, "PUT", path, `{"name":"all original targets","protocol":"tls","listen_port":21100,"ruleset_ids":[],"manual_domains":[]}`, 200)
	assertPolicy(nil, nil, true)
	f.request(t, "PUT", path, `{"name":"invalid fixed mode","protocol":"tls","listen_port":21100,"use_original_target":false,"targets":["fixed.test:443"],"manual_domains":["example.test"]}`, 400)
	assertPolicy(nil, nil, true)
	f.request(t, "PUT", path, `{"name":"fixed","protocol":"tls","listen_port":21100,"use_original_target":false,"targets":["fixed.test:443"]}`, 200)
	assertPolicy(nil, nil, false)
}
