package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
)

func TestRuleReconciliationRepairsStatusAndExitAllowlist(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &lifecycleNode{t: t}
	node := startServiceTestNode(t, remote)
	entry := model.GostNode{Name: "allowed source", Port: 39000, Address: "192.0.2.8", IPv4: "192.0.2.8", Status: model.NodeStatusOffline}
	saveServiceFixtures(t, db, &node, &entry)
	known := model.GostRule{Name: "tracked", NodeID: &node.ID, ListenPort: 8080, Status: model.RuleStatusRunning, ServiceID: "failed-service"}
	legacy := model.GostRule{Name: "configured outside database", NodeID: &node.ID, ListenPort: 8081, Status: model.RuleStatusStopped}
	saveServiceFixtures(t, db, &known, &legacy)
	tunnel := model.GostTunnel{Name: "exit relay", EntryNodeID: entry.ID, ExitNodeID: node.ID}
	saveServiceFixtures(t, db, &tunnel)
	tunnel.ServiceID = relayServiceName(tunnel.ID)
	requireServiceError(t, db.Save(&tunnel).Error, nil)
	relay := gost.ServiceConfig{Name: tunnel.ServiceID, Handler: &gost.HandlerConfig{Type: "relay"}}
	relayJSON, err := json.Marshal(relay)
	requireServiceError(t, err, nil)
	remote.mu.Lock()
	remote.resources = map[string]json.RawMessage{"/config/services/" + relay.Name: relayJSON}
	remote.gostConfig = fmt.Sprintf("{\"services\":[%s,{\"name\":\"failed-service\",\"status\":{\"state\":\"failed\"}},{\"name\":\"rule-%d\",\"status\":{\"state\":\"running\"}},{\"name\":\"untracked\"}]}", relayJSON, legacy.ID)
	remote.mu.Unlock()
	ctx := context.Background()
	cfg, err := NewGostAllowlistService(db).Update(ctx, []uint{entry.ID})
	requireServiceError(t, err, nil)
	syncService := NewRuleSyncService(db)
	syncService.Start()
	defer syncService.Stop()
	waitServiceCondition(t, func() bool {
		return remote.service(relay.Name).HasRelayAllowlist()
	})
	waitServiceCondition(t, func() bool {
		_, running := syncService.syncing.Load(node.ID)
		return !running
	})
	requireServiceError(t, db.First(&known, known.ID).Error, nil)
	requireServiceError(t, db.First(&legacy, legacy.ID).Error, nil)
	if known.Status != model.RuleStatusError || legacy.Status != model.RuleStatusRunning {
		t.Fatalf("reconciled statuses = %q, %q", known.Status, legacy.Status)
	}
	if admission := remote.admission(gost.RelayAllowlistName); !admission.Whitelist || !reflect.DeepEqual(admission.Matchers, cfg.Addresses) {
		t.Fatalf("reconciled admission = %+v", admission)
	}
	// Unmanaged relays must remain untouched when clearing the allowlist.
	client := utils.GetGostClient(&node)
	requireServiceError(t, syncGostAllowlist(client, &gost.GostConfig{Services: []gost.ServiceConfig{relay}}, node.ID, nil, cfg), nil)
}

func TestRulesetSyncRetriesSaveWithoutRecreatingBypass(t *testing.T) {
	db := newServiceTestDB(t)
	remote, offlineRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	node := startServiceTestNode(t, remote)
	offline := startServiceTestNode(t, offlineRemote)
	offline.Name, offline.Status = "offline", model.NodeStatusOffline
	saveServiceFixtures(t, db, &node, &offline)
	ctx := context.Background()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rules":
			_, _ = fmt.Fprint(w, "payload:\n  - DOMAIN-SUFFIX,example.com\n")
		case "/large":
			_, _ = fmt.Fprint(w, strings.Repeat("x", maxRulesetBytes+1))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	service := NewGostRulesetService(db)
	service.client = upstream.Client()
	for _, tc := range []struct {
		source string
		ids    []uint
	}{
		{"", []uint{node.ID}},
		{"http://rules.test", nil},
		{upstream.URL + "/rules", []uint{99}},
		{upstream.URL + "/not-found", nil},
		{upstream.URL + "/large", nil},
	} {
		if _, err := service.Update(ctx, tc.source, tc.ids); err == nil {
			t.Fatalf("invalid ruleset accepted: %+v", tc)
		}
	}
	settings, err := service.Update(ctx, upstream.URL+"/rules", []uint{offline.ID, node.ID, node.ID})
	requireServiceError(t, err, nil)
	if !reflect.DeepEqual(settings.NodeIDs, []uint{node.ID, offline.ID}) {
		t.Fatalf("saved node IDs = %v", settings.NodeIDs)
	}

	remote.mu.Lock()
	remote.reject = map[string]int{"POST /config?format=yaml": http.StatusServiceUnavailable}
	remote.mu.Unlock()
	pending := make(map[uint]bool)
	service.sync(ctx, pending)
	if !pending[node.ID] || pending[offline.ID] {
		t.Fatalf("unsaved nodes = %v", pending)
	}
	const path = "/config/bypasses/" + gost.TransparentBypassName
	var active struct {
		Name      string
		Whitelist bool
		Matchers  []string
	}
	remote.read(path, &active)
	if active.Name != gost.TransparentBypassName || !active.Whitelist || !reflect.DeepEqual(active.Matchers, settings.Matchers) {
		t.Fatalf("node bypass = %+v", active)
	}
	var skipped struct{ Name string }
	offlineRemote.read(path, &skipped)
	if skipped.Name != "" {
		t.Fatalf("offline node changed = %+v", skipped)
	}
	remote.mu.Lock()
	remote.reject = nil
	remote.mu.Unlock()
	service.sync(ctx, pending)
	if len(pending) != 0 {
		t.Fatalf("pending saves remained after retry: %v", pending)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	service.Run(canceled)
}
