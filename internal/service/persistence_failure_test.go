package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/internal/repository"
)

func TestServicePropagatesDatabaseReadFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("missing nodes table", func(t *testing.T) {
		db := newServiceTestDB(t)
		requireServiceError(t, db.Migrator().DropTable(&model.GostNode{}), nil)
		s := NewNodeService(db, "")
		_, createErr := s.Create(&dto.CreateNodeReq{Name: "node"}, testAdmin, testIP, testAgent)
		_, getErr := s.GetByID(1)
		_, updateErr := s.Update(1, &dto.UpdateNodeReq{Name: "renamed"}, testAdmin, testIP, testAgent)
		deleteErr := s.Delete(1, testAdmin, testIP, testAgent)
		_, tokenErr := s.IssueRegistrationToken(ctx, 1, testAdmin, testIP, testAgent)
		_, statsErr := NewStatsService(db).GetDashboardStats()
		_, upgradeErr := NewNodeUpgradeService(db).Get(ctx, 1)
		id := uint(1)
		_, ruleErr := NewRuleService(db).Create(&dto.CreateRuleReq{Type: "forward", NodeID: &id}, testAdmin, testIP, testAgent)
		_, tunnelErr := NewTunnelService(db).Create(&dto.CreateTunnelReq{EntryNodeID: 1, ExitNodeID: 2}, testAdmin, testIP, testAgent)
		_, allowlistErr := NewGostAllowlistService(db).Update(ctx, []uint{id})
		_, rulesetErr := NewGostRulesetService(db).Update(ctx, "https://rules.test", []uint{id})
		for _, err := range []error{createErr, getErr, updateErr, deleteErr, tokenErr, statsErr, upgradeErr, ruleErr, tunnelErr, allowlistErr, rulesetErr} {
			requireMissingTableError(t, err, "nodes")
		}
		NewNodeHealthService(db).checkNodes()
		NewRuleSyncService(db).syncAll()
		NewNodeUpgradeService(db).runAutomatic(ctx)
	})

	t.Run("missing rules table", func(t *testing.T) {
		db := newServiceTestDB(t)
		node, exit := model.GostNode{Name: "node", Port: 39000}, model.GostNode{Name: "exit", Port: 39000}
		saveServiceFixtures(t, db, &node, &exit)
		tunnel := model.GostTunnel{Name: "relay", EntryNodeID: node.ID, ExitNodeID: exit.ID}
		saveServiceFixtures(t, db, &tunnel)
		requireServiceError(t, db.Migrator().DropTable(&model.GostRule{}), nil)
		s := NewRuleService(db)
		_, createErr := s.Create(&dto.CreateRuleReq{Type: "forward", NodeID: &node.ID}, testAdmin, testIP, testAgent)
		_, getErr := s.GetByID(1)
		_, updateErr := s.Update(1, &dto.UpdateRuleReq{Name: "renamed"}, testAdmin, testIP, testAgent)
		deleteErr := s.Delete(1, testAdmin, testIP, testAgent)
		tunnelErr := NewTunnelService(db).Delete(tunnel.ID, testAdmin, testIP, testAgent)
		_, statsErr := NewStatsService(db).GetDashboardStats()
		observerErr := NewObserverService(db).RecordTraffic(ctx, node.ID, []TrafficSample{{RuleID: 1}})
		for _, err := range []error{createErr, getErr, updateErr, deleteErr, tunnelErr, statsErr, observerErr} {
			requireMissingTableError(t, err, "rules")
		}
	})

	t.Run("missing tunnels table", func(t *testing.T) {
		db := newServiceTestDB(t)
		node, exit := model.GostNode{Name: "node", Port: 39000}, model.GostNode{Name: "exit", Port: 39000}
		saveServiceFixtures(t, db, &node, &exit)
		requireServiceError(t, db.Migrator().DropTable(&model.GostTunnel{}), nil)
		s := NewTunnelService(db)
		_, createErr := s.Create(&dto.CreateTunnelReq{EntryNodeID: node.ID, ExitNodeID: exit.ID}, testAdmin, testIP, testAgent)
		_, getErr := s.GetByID(1)
		_, updateErr := s.Update(1, &dto.UpdateTunnelReq{Name: "renamed"}, testAdmin, testIP, testAgent)
		deleteErr := s.Delete(1, testAdmin, testIP, testAgent)
		id := uint(1)
		_, ruleErr := NewRuleService(db).Create(&dto.CreateRuleReq{Type: "tunnel", TunnelID: &id}, testAdmin, testIP, testAgent)
		_, statsErr := NewStatsService(db).GetDashboardStats()
		for _, err := range []error{createErr, getErr, updateErr, deleteErr, ruleErr, statsErr} {
			requireMissingTableError(t, err, "tunnels")
		}
	})
}

func TestServiceDoesNotAuditRejectedDatabaseWrites(t *testing.T) {
	db := newServiceTestDB(t)
	node, exit := model.GostNode{Name: "node", Port: 39000}, model.GostNode{Name: "exit", Port: 39000}
	saveServiceFixtures(t, db, &node, &exit)
	rule := model.GostRule{Name: "forward", NodeID: &node.ID, ListenPort: 8080}
	tunnel := model.GostTunnel{Name: "relay", EntryNodeID: node.ID, ExitNodeID: exit.ID}
	saveServiceFixtures(t, db, &rule, &tunnel)
	for _, table := range []string{"nodes", "rules", "tunnels"} {
		for _, event := range []string{"INSERT", "UPDATE"} {
			sql := fmt.Sprintf("CREATE TRIGGER reject_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'test write rejected'); END", table, event, event, table)
			requireServiceError(t, db.Exec(sql).Error, nil)
		}
	}
	nodes, rules, tunnels := NewNodeService(db, ""), NewRuleService(db), NewTunnelService(db)
	_, createNode := nodes.Create(&dto.CreateNodeReq{Name: "new node"}, testAdmin, testIP, testAgent)
	_, updateNode := nodes.Update(node.ID, &dto.UpdateNodeReq{Name: "renamed"}, testAdmin, testIP, testAgent)
	_, issueToken := nodes.IssueRegistrationToken(context.Background(), node.ID, testAdmin, testIP, testAgent)
	_, createRule := rules.Create(&dto.CreateRuleReq{Name: "new rule", Type: "forward", NodeID: &node.ID, ListenPort: 8081}, testAdmin, testIP, testAgent)
	_, updateRule := rules.Update(rule.ID, &dto.UpdateRuleReq{Name: "renamed", ListenPort: 8081}, testAdmin, testIP, testAgent)
	deleteRule := rules.Delete(rule.ID, testAdmin, testIP, testAgent)
	_, createTunnel := tunnels.Create(&dto.CreateTunnelReq{Name: "new tunnel", EntryNodeID: node.ID, ExitNodeID: exit.ID}, testAdmin, testIP, testAgent)
	_, updateTunnel := tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "renamed"}, testAdmin, testIP, testAgent)
	deleteTunnel := tunnels.Delete(tunnel.ID, testAdmin, testIP, testAgent)
	for _, err := range []error{createNode, updateNode, issueToken, createRule, updateRule, deleteRule, createTunnel, updateTunnel, deleteTunnel} {
		requireWriteRejected(t, err)
	}
	var auditCount int64
	requireServiceError(t, db.Model(&model.OperationLog{}).Count(&auditCount).Error, nil)
	if auditCount != 0 {
		t.Fatalf("failed writes emitted %d successful audit entries", auditCount)
	}
	requireServiceError(t, db.Exec("DROP TRIGGER reject_rules_UPDATE").Error, nil)
	requireServiceError(t, db.Exec("DROP TRIGGER reject_tunnels_UPDATE").Error, nil)
	requireServiceError(t, db.Delete(&rule).Error, nil)
	requireServiceError(t, db.Delete(&tunnel).Error, nil)
	requireWriteRejected(t, nodes.Delete(node.ID, testAdmin, testIP, testAgent))
}

func TestObserverRollsBackCountersWhenDatabaseRejectsWrite(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestDB(t)
	node := model.GostNode{Name: "node", Port: 39000}
	saveServiceFixtures(t, db, &node)
	rule := model.GostRule{Name: "forward", NodeID: &node.ID, ListenPort: 8080}
	saveServiceFixtures(t, db, &rule)
	service := NewObserverService(db)
	sample := []TrafficSample{{RuleID: rule.ID, ObserverCounters: model.ObserverCounters{InputBytes: 20, OutputBytes: 10, Connections: 1}}}
	for _, table := range []string{"nodes", "rules"} {
		requireServiceError(t, db.Exec("CREATE TRIGGER reject_sample BEFORE UPDATE ON "+table+" BEGIN SELECT RAISE(ABORT, 'test write rejected'); END").Error, nil)
		requireWriteRejected(t, service.RecordTraffic(ctx, node.ID, sample))
		requireServiceError(t, db.Exec("DROP TRIGGER reject_sample").Error, nil)
		requireServiceError(t, db.First(&rule, rule.ID).Error, nil)
		requireServiceError(t, db.First(&node, node.ID).Error, nil)
		if rule.TotalBytes != 0 || rule.ObserverCounters.InputBytes != 0 || node.TotalBytes != 0 {
			t.Fatalf("failed write to %s advanced observed or aggregate traffic", table)
		}
	}
	requireServiceError(t, service.RecordTraffic(ctx, node.ID, sample), nil)
	saved, err := repository.NewRuleRepository(db).FindByID(rule.ID)
	requireServiceError(t, err, nil)
	if saved.TotalBytes != 30 {
		t.Fatalf("successful retry recorded %d bytes, want 30", saved.TotalBytes)
	}
}

func requireMissingTableError(t *testing.T, err error, table string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "no such table: "+table) {
		t.Fatalf("database error = %v, want missing %q table", err, table)
	}
}

func requireWriteRejected(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "test write rejected") {
		t.Fatalf("database error = %v, want rejected write", err)
	}
}
