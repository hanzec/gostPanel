package repository

import (
	"slices"
	"testing"

	"yagp/internal/model"
)

func TestDashboardTrafficKeepsOneRecordPerTunnel(t *testing.T) {
	db := openRepositoryDB(t)
	tunnels := NewTunnelRepository(db)
	traffic, err := tunnels.ExitTraffic()
	mustPersist(t, err)
	if traffic == nil || len(traffic) != 0 {
		t.Fatalf("expected an empty exit list, got %+v", traffic)
	}

	nodes := []model.GostNode{
		{Name: "entry only", Port: 39100},
		{Name: "shared exit", Port: 39101, Status: model.NodeStatusOnline},
		{Name: "offline exit", Port: 39102, Status: model.NodeStatusOffline},
		{Name: "deleted exit", Port: 39103},
	}
	mustPersist(t, db.Create(&nodes).Error)
	entry, shared, offline, deleted := nodes[0], nodes[1], nodes[2], nodes[3]
	rows := []model.GostTunnel{
		{Name: "observed running", EntryNodeID: entry.ID, ExitNodeID: shared.ID, Status: model.TunnelStatusRunning},
		{Name: "observed stopped", EntryNodeID: entry.ID, ExitNodeID: shared.ID, Status: model.TunnelStatusStopped},
		{Name: "pending", EntryNodeID: entry.ID, ExitNodeID: shared.ID, Status: model.TunnelStatusRunning},
		{Name: "never started", EntryNodeID: entry.ID, ExitNodeID: offline.ID, Status: model.TunnelStatusStopped},
		{Name: "deleted exit", EntryNodeID: entry.ID, ExitNodeID: deleted.ID, Status: model.TunnelStatusRunning},
		{Name: "deleted tunnel", EntryNodeID: entry.ID, ExitNodeID: shared.ID, Status: model.TunnelStatusRunning},
	}
	mustPersist(t, db.Create(&rows).Error)
	mustPersist(t, tunnels.RecordRelayTraffic(rows[0].ID, 10, 20, 10, 20, 100))
	mustPersist(t, tunnels.RecordRelayTraffic(rows[0].ID, 30, 40, 30, 40, 200))
	mustPersist(t, tunnels.RecordRelayTraffic(rows[1].ID, 3, 6, 3, 6, 100))
	mustPersist(t, tunnels.RecordRelayTraffic(rows[4].ID, 80, 90, 80, 90, 100))
	mustPersist(t, tunnels.RecordRelayTraffic(rows[5].ID, 100, 200, 100, 200, 100))
	mustPersist(t, db.Delete(&deleted).Error)
	mustPersist(t, tunnels.Delete(rows[5].ID))

	stored, err := tunnels.FindByID(rows[0].ID)
	mustPersist(t, err)
	wantCheckpoint := model.RelayTraffic{InputBytes: 40, OutputBytes: 60, LastInputBytes: 30, LastOutputBytes: 40, ServiceCreatedAt: 200}
	if stored.RelayTraffic != wantCheckpoint || !stored.UpdatedAt.Equal(rows[0].UpdatedAt) {
		t.Fatalf("stored checkpoint = %+v, updated_at = %v", stored.RelayTraffic, stored.UpdatedAt)
	}

	traffic, err = tunnels.ExitTraffic()
	mustPersist(t, err)
	want := []TunnelExitTraffic{
		{ID: rows[0].ID, Name: rows[0].Name, Status: rows[0].Status, ExitNodeID: shared.ID, ExitNodeName: shared.Name, ExitNodeStatus: shared.Status, TrafficTotals: TrafficTotals{InputBytes: 40, OutputBytes: 60}, Measured: true},
		{ID: rows[1].ID, Name: rows[1].Name, Status: rows[1].Status, ExitNodeID: shared.ID, ExitNodeName: shared.Name, ExitNodeStatus: shared.Status, TrafficTotals: TrafficTotals{InputBytes: 3, OutputBytes: 6}, Measured: true},
		{ID: rows[2].ID, Name: rows[2].Name, Status: rows[2].Status, ExitNodeID: shared.ID, ExitNodeName: shared.Name, ExitNodeStatus: shared.Status},
		{ID: rows[3].ID, Name: rows[3].Name, Status: rows[3].Status, ExitNodeID: offline.ID, ExitNodeName: offline.Name, ExitNodeStatus: offline.Status},
	}
	if !slices.Equal(traffic, want) {
		t.Fatalf("exit traffic = %+v, want %+v", traffic, want)
	}
}

func TestRuleMoveRollsBackRelayArchiveWhenUpdateFails(t *testing.T) {
	db := openRepositoryDB(t)
	nodes := []model.GostNode{{Name: "source"}, {Name: "destination"}}
	mustPersist(t, db.Create(&nodes).Error)
	source, destination := nodes[0].ID, nodes[1].ID
	rule := model.GostRule{Name: "exit", Role: model.EndpointRoleExit, NodeID: &source, ListenPort: 8443}
	rules := NewRuleRepository(db)
	mustPersist(t, rules.Create(&rule))
	mustPersist(t, rules.RecordExitRelayTraffic(rule.ID, "relay-tunnel-1", 30, 40, 30, 40, 100))
	mustPersist(t, db.Exec(`CREATE TRIGGER fail_exit_move BEFORE UPDATE OF node_id ON rules
		BEGIN SELECT RAISE(ABORT, 'rule update failed'); END`).Error)

	rule.NodeID = &destination
	if err := rules.Update(&rule); err == nil {
		t.Fatal("expected the reassignment to fail")
	}
	stored, err := rules.FindByID(rule.ID)
	mustPersist(t, err)
	if stored.NodeID == nil || *stored.NodeID != source ||
		stored.RelayTraffic != (model.RelayTraffic{InputBytes: 30, OutputBytes: 40, LastInputBytes: 30, LastOutputBytes: 40, ServiceCreatedAt: 100}) ||
		stored.RelayCounterID != "relay-tunnel-1" {
		t.Fatalf("reassignment changed stored rule and checkpoint: %+v", stored)
	}
	usage, err := NewNodeRepository(db).RelayUsageByNode(t.Context(), []uint{source, destination})
	mustPersist(t, err)
	if len(usage) != 1 || usage[source] != 70 {
		t.Fatalf("failed reassignment changed node usage: %v", usage)
	}
}
