package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
)

func TestGostSubscriptionsPreserveLegacyIDAndRefreshIndependently(t *testing.T) {
	var payloads atomic.Value
	payloads.Store(map[string]string{"/first": "DOMAIN-SUFFIX,first.example", "/second": "DOMAIN-SUFFIX,second.example"})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, payloads.Load().(map[string]string)[r.URL.Path])
	}))
	defer upstream.Close()
	svc := NewGostRulesetService(newServiceTestDB(t))
	svc.client.Transport = upstream.Client().Transport
	first, err := svc.Create(t.Context(), " First ", upstream.URL+"/first")
	requireServiceError(t, err, nil)
	second, err := svc.Create(t.Context(), "Second", upstream.URL+"/second")
	requireServiceError(t, err, nil)
	legacy, err := svc.Get(t.Context())
	requireServiceError(t, err, nil)
	if first.ID != 2 || second.ID != 3 || first.Name != "First" || legacy.ID != 1 || legacy.URL != "" {
		t.Fatalf("named subscriptions reused the legacy ID: %+v, %+v, %+v", first, second, legacy)
	}
	list, err := svc.List(t.Context())
	requireServiceError(t, err, nil)
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("unexpected subscription list: %+v", list)
	}
	payloads.Store(map[string]string{"/first": "DOMAIN-SUFFIX,new.example", "/second": "RULE-SET,unsupported"})
	svc.refresh(t.Context())
	list, err = svc.List(t.Context())
	requireServiceError(t, err, nil)
	if !slices.Equal(list[0].Matchers, []string{".new.example"}) || !slices.Equal(list[1].Matchers, second.Matchers) ||
		!list[1].UpdatedAt.Equal(*second.UpdatedAt) {
		t.Fatalf("refresh did not independently retain the last valid subscriptions: %+v", list)
	}
	updated, err := svc.UpdateByID(t.Context(), first.ID, "Renamed", upstream.URL+"/first")
	requireServiceError(t, err, nil)
	if updated.Name != "Renamed" || updated.ID != first.ID {
		t.Fatalf("subscription rename changed identity: %+v", updated)
	}
	if _, err := svc.UpdateByID(t.Context(), first.ID, "Invalid", ""); err == nil {
		t.Fatal("named subscription accepted an empty URL")
	}
	legacy.NodeIDs = []uint{42}
	requireServiceError(t, svc.db.Save(legacy).Error, nil)
	legacy, err = svc.UpdateByID(t.Context(), 1, "Legacy renamed", upstream.URL+"/first")
	requireServiceError(t, err, nil)
	if !slices.Equal(legacy.NodeIDs, []uint{42}) {
		t.Fatalf("plural update discarded legacy node assignments: %+v", legacy)
	}
}

func TestGostRulesetSelectionNormalizesAndCombines(t *testing.T) {
	db := newServiceTestDB(t)
	saveServiceFixtures(t, db,
		&model.GostRuleset{ID: 1},
		&model.GostRuleset{ID: 2, URL: "https://first.example/rules", Matchers: []string{".first.example", ".shared.example"}},
		&model.GostRuleset{ID: 3, URL: "https://second.example/rules", Matchers: []string{".second.example", ".shared.example"}})
	svc := NewGostRulesetService(db)
	input := []uint{3, 2, 3}
	ids, domains, err := svc.NormalizeSelection(t.Context(), input, []string{" Example.COM. ", "shared.example", "EXAMPLE.com"})
	requireServiceError(t, err, nil)
	if !slices.Equal(ids, []uint{2, 3}) || !slices.Equal(domains, []string{"example.com", "shared.example"}) ||
		!slices.Equal(input, []uint{3, 2, 3}) {
		t.Fatalf("selection normalization = %v, %v; input %v", ids, domains, input)
	}
	matchers, err := svc.SelectedMatchers(t.Context(), ids, domains)
	requireServiceError(t, err, nil)
	if !slices.Equal(matchers, []string{".first.example", ".shared.example", ".second.example", ".example.com"}) {
		t.Fatalf("merged matchers = %v", matchers)
	}
	for _, ids := range [][]uint{{0}, {1}, {99}} {
		_, _, err := svc.NormalizeSelection(t.Context(), ids, nil)
		if biz, ok := err.(*bizerrors.BizError); !ok || biz.HTTPCode != http.StatusBadRequest || biz.Code != 10421 {
			t.Fatalf("unavailable ruleset IDs %v returned %v", ids, err)
		}
	}
	for _, domain := range []string{"", "192.0.2.1", "https://example.com", "*.example.com", ".example.com", "example.com:443", "bad name.example", "-bad.example", "例子.example"} {
		if _, _, err := svc.NormalizeSelection(t.Context(), nil, []string{domain}); err == nil {
			t.Fatalf("accepted invalid manual domain %q", domain)
		}
	}
	if _, _, err := svc.NormalizeSelection(t.Context(), make([]uint, 501), nil); err == nil {
		t.Fatal("accepted too many selections")
	}
	matchers, err = svc.SelectedMatchers(t.Context(), nil, nil)
	requireServiceError(t, err, nil)
	if matchers == nil || len(matchers) != 0 {
		t.Fatalf("empty selection invented a matcher: %v", matchers)
	}
	requireServiceError(t, db.Migrator().DropTable(&model.GostRuleset{}), nil)
	_, _, err = svc.NormalizeSelection(t.Context(), []uint{2}, nil)
	if _, ok := err.(*bizerrors.BizError); err == nil || ok {
		t.Fatalf("database failure was disguised as invalid selection: %v", err)
	}
}

func TestGostSubscriptionsRejectDeletingReferencesAndClearingSelectedDefault(t *testing.T) {
	db := newServiceTestDB(t)
	saveServiceFixtures(t, db,
		&model.GostRuleset{ID: 1, URL: "https://default.example/rules", Matchers: []string{".default.example"}},
		&model.GostRuleset{ID: 2, URL: "https://first.example/rules", Matchers: []string{".first.example"}},
		&model.GostRuleset{ID: 3, URL: "https://second.example/rules", Matchers: []string{".second.example"}})
	rule := model.GostRule{Name: "Selected endpoint", RulesetIDs: []uint{1, 2}}
	tunnel := model.GostTunnel{Name: "Selected tunnel", RulesetIDs: []uint{3}}
	saveServiceFixtures(t, db, &rule, &tunnel)
	svc := NewGostRulesetService(db)
	for _, id := range []uint{2, 3} {
		if err := svc.Delete(t.Context(), id); err == nil || !strings.Contains(err.Error(), "移除引用") {
			t.Fatalf("deleting referenced subscription %d returned %v", id, err)
		}
	}
	if err := svc.Delete(t.Context(), 1); err == nil || !strings.Contains(err.Error(), "默认规则集不能删除") {
		t.Fatalf("catalog deletion did not protect the legacy default: %v", err)
	}
	if _, err := svc.Update(t.Context(), "", nil); err == nil {
		t.Fatal("legacy update cleared an explicitly selected subscription")
	}
	legacy, err := svc.Get(t.Context())
	requireServiceError(t, err, nil)
	if legacy.URL == "" || len(legacy.Matchers) == 0 {
		t.Fatal("failed clear changed the selected default subscription")
	}
	requireServiceError(t, db.Delete(&rule).Error, nil)
	requireServiceError(t, svc.Delete(t.Context(), 2), nil)
	_, err = svc.Update(t.Context(), "", nil)
	requireServiceError(t, err, nil)
}

func TestGostSelectedPoliciesSyncWithoutGlobalSubscriptionAndRetainOnFailure(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	remote := &lifecycleNode{t: t}
	entry := startServiceTestNode(t, remote)
	entryRule := model.GostRule{Name: "Transparent", NodeID: &entry.ID, Role: model.EndpointRoleEntry,
		Protocol: model.RuleProtocolTransparent, ListenIP: "127.0.0.1", ListenPort: 12345, Status: model.RuleStatusRunning}
	exitRule := model.GostRule{Name: "Selected policy", Role: model.EndpointRoleExit, UseOriginalTarget: true,
		RulesetIDs: []uint{2}, ManualDomains: []string{"manual.example"}}
	subscription := model.GostRuleset{ID: 2, URL: "https://selected.example/rules", Matchers: []string{".old.example"}}
	saveServiceFixtures(t, db, &entry, &entryRule, &exitRule, &subscription)
	tunnel := model.GostTunnel{Name: "Selected transparent", EntryNodeID: entry.ID, EntryRuleID: &entryRule.ID,
		ExitRuleID: &exitRule.ID, Status: model.TunnelStatusRunning, RulesetIDs: []uint{999}}
	saveServiceFixtures(t, db, &tunnel)
	svc := NewGostRulesetService(db)
	unsaved := make(map[uint]bool)
	svc.sync(t.Context(), unsaved)
	read := func() []string {
		var bypass gost.AdmissionConfig
		remote.read("/config/bypasses/"+transparentPolicyName(tunnel.ID), &bypass)
		return bypass.Matchers
	}
	if !slices.Equal(read(), []string{".old.example", ".manual.example"}) {
		t.Fatalf("selected exit policy was not applied independently of global rules: %v", read())
	}
	subscription.Matchers = []string{".updated.example"}
	requireServiceError(t, db.Save(&subscription).Error, nil)
	remote.rejectCommand("POST /config?format=yaml", http.StatusBadGateway)
	svc.sync(t.Context(), unsaved)
	if !unsaved[entry.ID] || !slices.Equal(read(), []string{".updated.example", ".manual.example"}) {
		t.Fatal("changed subscription was not retained for a config-save retry")
	}
	remote.rejectCommand("POST /config?format=yaml", 0)
	svc.sync(t.Context(), unsaved)
	if unsaved[entry.ID] {
		t.Fatal("successful retry left the node dirty")
	}
	requireServiceError(t, db.Delete(&subscription).Error, nil)
	svc.sync(t.Context(), unsaved)
	if !slices.Equal(read(), []string{".updated.example", ".manual.example"}) {
		t.Fatal("missing subscription erased the last applied selection")
	}
}
