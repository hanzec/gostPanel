package service

import (
	"net/http"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
)

func TestEntranceBindingAppearsInDetailsAndFilteredLists(t *testing.T) {
	db := newServiceTestDB(t)
	entry := model.GostNode{Name: "entry", Port: 39101}
	exit := model.GostNode{Name: "exit", Port: 39102}
	saveServiceFixtures(t, db, &entry, &exit)
	first := model.GostRule{Name: "entry listener", NodeID: &entry.ID, ListenPort: 8443}
	second := model.GostRule{Name: "exit listener", NodeID: &exit.ID, ListenPort: 8443}
	saveServiceFixtures(t, db, &first, &second)
	tunnel := model.GostTunnel{Name: "selected tunnel", EntryNodeID: entry.ID, ExitNodeID: exit.ID, EntryRuleID: &first.ID, ExitRuleID: &second.ID}
	saveServiceFixtures(t, db, &tunnel)

	rules := NewRuleService(db)
	assertBinding := func(rule *model.GostRule, role string) {
		t.Helper()
		if rule.LinkedTunnelID == nil || *rule.LinkedTunnelID != tunnel.ID || rule.TunnelRole != role {
			t.Fatalf("rule %d binding = (%v, %q), want tunnel %d (%s)", rule.ID, rule.LinkedTunnelID, rule.TunnelRole, tunnel.ID, role)
		}
	}
	for _, endpoint := range []struct {
		rule model.GostRule
		role string
	}{{first, "entry"}, {second, "exit"}} {
		id := endpoint.rule.ID
		detail, err := rules.GetByID(id)
		requireServiceError(t, err, nil)
		assertBinding(detail, endpoint.role)

		filtered, total, err := rules.List(&dto.RuleListReq{NodeID: *endpoint.rule.NodeID, PageSize: 1})
		requireServiceError(t, err, nil)
		if total != 1 || len(filtered) != 1 || filtered[0].ID != id {
			t.Fatalf("filtered list = %+v, total %d", filtered, total)
		}
		assertBinding(&filtered[0], endpoint.role)

		_, err = rules.Update(id, &dto.UpdateRuleReq{Name: "must stay bound"}, testAdmin, testIP, testAgent)
		requireServiceError(t, err, bizerrors.ErrRuleTunnelLinked)
		requireServiceError(t, rules.Start(id, testAdmin, testIP, testAgent), bizerrors.ErrRuleTunnelLinked)
		requireServiceError(t, rules.Stop(id, testAdmin, testIP, testAgent), bizerrors.ErrRuleTunnelLinked)
		requireServiceError(t, rules.Delete(id, testAdmin, testIP, testAgent), bizerrors.ErrRuleTunnelLinked)
	}

	requireServiceError(t, db.Migrator().DropTable(&model.GostTunnel{}), nil)
	_, err := rules.GetByID(first.ID)
	requireMissingTableError(t, err, "tunnels")
	_, _, err = rules.List(&dto.RuleListReq{})
	requireMissingTableError(t, err, "tunnels")
	requireMissingTableError(t, rules.Start(first.ID, testAdmin, testIP, testAgent), "tunnels")
	requireMissingTableError(t, rules.Stop(first.ID, testAdmin, testIP, testAgent), "tunnels")
}

func TestStandaloneEntranceUpdateValidatesSelectedNodeAndListener(t *testing.T) {
	db := newServiceTestDB(t)
	first := model.GostNode{Name: "first", Port: 39101}
	second := model.GostNode{Name: "second", Port: 39102}
	saveServiceFixtures(t, db, &first, &second)
	rules := NewRuleService(db)
	created, err := rules.Create(&dto.CreateRuleReq{NodeID: &first.ID, Name: "source", Protocol: "tcp", ListenPort: 8443}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	other, err := rules.Create(&dto.CreateRuleReq{NodeID: &second.ID, Name: "exit-only", Protocol: "tcp", ListenPort: 8443}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, rules.Start(other.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleTargetRequired)

	unknown := second.ID + 10
	invalidIP := "bad listen address"
	clash := true
	for _, scenario := range []struct {
		req  dto.UpdateRuleReq
		want error
	}{
		{dto.UpdateRuleReq{NodeID: &unknown}, bizerrors.ErrNodeNotFound},
		{dto.UpdateRuleReq{ListenIP: &invalidIP}, bizerrors.ErrRuleListenIPInvalid},
		{dto.UpdateRuleReq{UseClashRuleset: &clash, Protocol: "udp"}, bizerrors.ErrRuleClashInvalid},
		{dto.UpdateRuleReq{NodeID: &second.ID, Protocol: "tcp", ListenPort: 8443}, bizerrors.ErrRulePortExists},
	} {
		_, err = rules.Update(created.ID, &scenario.req, testAdmin, testIP, testAgent)
		requireServiceError(t, err, scenario.want)
	}
	listenIP := " ::ffff:127.0.0.1 "
	updated, err := rules.Update(created.ID, &dto.UpdateRuleReq{
		NodeID: &second.ID, Name: "moved", Protocol: "tcp", ListenIP: &listenIP, ListenPort: 8444, UseClashRuleset: &clash,
	}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if updated.NodeID == nil || *updated.NodeID != second.ID || updated.ListenIP != "127.0.0.1" || !updated.UseClashRuleset {
		t.Fatalf("moved listener = %+v", updated)
	}
	requireServiceError(t, rules.Start(updated.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleTargetRequired)
	stored, err := rules.GetByID(updated.ID)
	requireServiceError(t, err, nil)
	if stored.Node == nil || stored.Node.ID != second.ID || stored.Status != model.RuleStatusStopped || stored.LinkedTunnelID != nil {
		t.Fatalf("stored listener = %+v", stored)
	}
}

func TestSelectedEntranceReportsNodeConfigurationFailures(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	remote := &lifecycleNode{t: t}
	entry := startServiceTestNode(t, remote)
	saveServiceFixtures(t, db, &entry)
	rule := model.GostRule{Name: "entry listener", NodeID: &entry.ID, ListenPort: 8443, Targets: []string{"origin.test:443"}}
	saveServiceFixtures(t, db, &rule)
	tunnel := model.GostTunnel{ChainID: "ready-chain"}
	rules := NewRuleService(db)
	setFailures := func(failures map[string]int) {
		remote.mu.Lock()
		remote.reject = failures
		remote.mu.Unlock()
	}

	setFailures(map[string]int{"POST /config?format=yaml": http.StatusServiceUnavailable})
	requireServiceError(t, rules.startSelectedEntrance(&rule, &entry, &tunnel, nil), bizerrors.ErrRuleStartFailed)
	stored, err := rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if stored.Status != model.RuleStatusError || stored.ServiceID != rule.ServiceID || rule.ServiceID == "" || remote.service(rule.ServiceID).Name == "" {
		t.Fatalf("failed start did not retain service information for tunnel rollback: rule=%+v stored=%+v", rule, stored)
	}

	setFailures(map[string]int{"DELETE /config/services/" + rule.ServiceID: http.StatusServiceUnavailable})
	if err := rules.stopSelectedEntrance(&rule); err == nil || remote.service(rule.ServiceID).Name == "" {
		t.Fatalf("expected failed service removal to be reported, got %v", err)
	}
	setFailures(map[string]int{"POST /config?format=yaml": http.StatusServiceUnavailable})
	if err := rules.stopSelectedEntrance(&rule); err == nil || remote.service(rule.ServiceID).Name != "" {
		t.Fatalf("expected failed config save after removing service, got %v", err)
	}
	setFailures(nil)
	requireServiceError(t, rules.stopSelectedEntrance(&rule), nil)
	if rule.Status != model.RuleStatusStopped {
		t.Fatalf("stopped entrance = %+v", rule)
	}
}
