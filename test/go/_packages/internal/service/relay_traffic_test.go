package service

import (
	"encoding/json"
	"slices"
	"testing"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/pkg/gost"
)

func TestDashboardUsesActualExitRelayTraffic(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &lifecycleNode{t: t}
	exit := startServiceTestNode(t, remote)
	entry := model.GostNode{Name: "entry", Port: 39000}
	other := model.GostNode{Name: "other exit", Port: 39001}
	saveServiceFixtures(t, db, &exit, &entry, &other)
	running := model.GostTunnel{Name: "active relay", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Status: model.TunnelStatusRunning}
	legacy := model.GostTunnel{Name: "relay with stats disabled", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Status: model.TunnelStatusRunning}
	stopped := model.GostTunnel{Name: "stopped relay", EntryNodeID: entry.ID, ExitNodeID: other.ID}
	saveServiceFixtures(t, db, &running, &legacy, &stopped)
	rule := model.GostRule{Name: "proxied and direct traffic", Type: model.RuleTypeTunnel, TunnelID: &running.ID, ListenPort: 8080, InputBytes: 900, OutputBytes: 1200}
	saveServiceFixtures(t, db, &rule)

	sync := NewRuleSyncService(db)
	statsService := NewStatsService(db)
	sample := func(created int64, input, output uint64) {
		t.Helper()
		cfg := gost.GostConfig{Services: []gost.ServiceConfig{
			{Name: relayServiceName(running.ID), Handler: &gost.HandlerConfig{Type: "relay"}, Status: &gost.ServiceStatus{
				CreateTime: created, State: "running", Stats: &gost.ServiceTrafficStats{InputBytes: input, OutputBytes: output},
			}},
			{Name: relayServiceName(legacy.ID), Handler: &gost.HandlerConfig{Type: "relay"}, Status: &gost.ServiceStatus{CreateTime: created, State: "running"}},
			{Name: relayServiceName(stopped.ID), Handler: &gost.HandlerConfig{Type: "relay"}, Status: &gost.ServiceStatus{
				CreateTime: created, State: "running", Stats: &gost.ServiceTrafficStats{InputBytes: 800},
			}},
		}}
		data, err := json.Marshal(cfg)
		requireServiceError(t, err, nil)
		remote.mu.Lock()
		remote.gostConfig = string(data)
		remote.mu.Unlock()
		sync.syncNodeRules(exit, nil)
	}
	sample(100, 110, 230)
	sample(100, 110, 230)
	sample(100, 180, 320)
	// A new instance's counters start from zero even if its first observed values exceed the old ones.
	sample(120, 200, 500)
	sample(120, 0, 0)
	sample(120, 30, 40)
	requireServiceError(t, repository.NewTunnelRepository(db).RecordRelayTraffic(stopped.ID, 12, 34, 12, 34, 50), nil)

	stats, err := statsService.GetDashboardStats()
	requireServiceError(t, err, nil)
	if stats.Traffic.TrafficTotals != (repository.TrafficTotals{InputBytes: 422, OutputBytes: 894}) {
		t.Fatalf("global relay totals = %+v", stats.Traffic.TrafficTotals)
	}
	if got := stats.Traffic.Tunnels; len(got) != 3 || got[0] != (repository.TunnelExitTraffic{
		ID: running.ID, Name: running.Name, Status: running.Status, ExitNodeID: exit.ID,
		ExitNodeName: exit.Name, ExitNodeStatus: model.NodeStatusOnline, Measured: true,
		TrafficTotals: repository.TrafficTotals{InputBytes: 410, OutputBytes: 860},
	}) || got[1].ID != legacy.ID || got[1].Measured || got[2].ExitNodeID != other.ID || got[2].InputBytes != 12 {
		t.Fatalf("exit totals = %+v", got)
	}
	// Saving an older settings form must not replace the last relay checkpoint.
	running.Remark = "new remark"
	requireServiceError(t, repository.NewTunnelRepository(db).Update(&running, false), nil)
	requireServiceError(t, db.First(&running, running.ID).Error, nil)
	if running.RelayTraffic.InputBytes != 410 || running.RelayTraffic.LastInputBytes != 30 {
		t.Fatalf("saved relay checkpoint = %+v", running.RelayTraffic)
	}
}

func TestDashboardGroupsRelayTrafficByExitEndpoint(t *testing.T) {
	db := newServiceTestDB(t)
	entryA := model.GostNode{Name: "entry A"}
	entryB := model.GostNode{Name: "entry B"}
	exitNode := model.GostNode{Name: "exit node", Status: model.NodeStatusOnline}
	saveServiceFixtures(t, db, &entryA, &entryB, &exitNode)
	entry := model.GostRule{Name: "entry A", NodeID: &entryA.ID, InputBytes: 900, OutputBytes: 1200, ListenPort: 8001}
	shared := model.GostRule{Name: "shared Relay", NodeID: &exitNode.ID, Role: model.EndpointRoleExit, ListenPort: 9001}
	distinct := model.GostRule{Name: "second Relay on same node", NodeID: &exitNode.ID, Role: model.EndpointRoleExit, ListenPort: 9002}
	saveServiceFixtures(t, db, &entry, &shared, &distinct)
	a := model.GostTunnel{Name: "tunnel A", EntryNodeID: entryA.ID, EntryRuleID: &entry.ID, ExitNodeID: exitNode.ID, ExitRuleID: &shared.ID, Status: model.TunnelStatusRunning}
	b := model.GostTunnel{Name: "tunnel B", EntryNodeID: entryB.ID, ExitNodeID: exitNode.ID, ExitRuleID: &shared.ID, Status: model.TunnelStatusStopped}
	c := model.GostTunnel{Name: "tunnel C", EntryNodeID: entryA.ID, ExitNodeID: exitNode.ID, ExitRuleID: &distinct.ID, Status: model.TunnelStatusRunning}
	legacy := model.GostTunnel{Name: "legacy Relay", EntryNodeID: entryA.ID, ExitNodeID: exitNode.ID, Status: model.TunnelStatusRunning}
	saveServiceFixtures(t, db, &a, &b, &c, &legacy)

	tunnels := repository.NewTunnelRepository(db)
	requireServiceError(t, tunnels.RecordRelayTraffic(a.ID, 100, 250, 100, 250, 1), nil)
	requireServiceError(t, tunnels.RecordRelayTraffic(b.ID, 10, 25, 10, 25, 2), nil)
	requireServiceError(t, tunnels.RecordRelayTraffic(legacy.ID, 1, 5, 1, 5, 3), nil)
	stats, err := NewStatsService(db).GetDashboardStats()
	requireServiceError(t, err, nil)

	wantNames := []string{shared.Name, distinct.Name, legacy.Name}
	var names []string
	for _, exit := range stats.Traffic.Exits {
		names = append(names, exit.Name)
		if !exit.Running || exit.ExitNodeID != exitNode.ID || exit.ExitNodeName != exitNode.Name {
			t.Errorf("unexpected exit metadata %+v", exit)
		}
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("exit names = %v, want %v", names, wantNames)
	}
	got := stats.Traffic.Exits
	if got[0].TrafficTotals != (repository.TrafficTotals{InputBytes: 110, OutputBytes: 275}) ||
		!got[0].Measured || got[1].Measured || !got[2].Measured ||
		stats.Traffic.TrafficTotals != (repository.TrafficTotals{InputBytes: 111, OutputBytes: 280}) {
		t.Fatalf("exit counters = %+v; total = %+v", got, stats.Traffic.TrafficTotals)
	}
}

func TestSharedExitTrafficContinuesAfterOriginalTunnelIsDeleted(t *testing.T) {
	db := newServiceTestDB(t)
	entry := model.GostNode{Name: "entry"}
	exit := model.GostNode{Name: "exit", Status: model.NodeStatusOnline}
	saveServiceFixtures(t, db, &entry, &exit)
	endpoint := model.GostRule{Name: "shared exit", NodeID: &exit.ID, Role: model.EndpointRoleExit, ListenPort: 9001}
	saveServiceFixtures(t, db, &endpoint)
	owner := model.GostTunnel{Name: "original tunnel", EntryNodeID: entry.ID, ExitNodeID: exit.ID, ExitRuleID: &endpoint.ID, Status: model.TunnelStatusRunning}
	saveServiceFixtures(t, db, &owner)
	serviceName := relayServiceName(owner.ID)
	requireServiceError(t, db.Model(&endpoint).Update("service_id", serviceName).Error, nil)
	remaining := model.GostTunnel{Name: "remaining tunnel", EntryNodeID: entry.ID, ExitNodeID: exit.ID, ExitRuleID: &endpoint.ID, ServiceID: serviceName, Status: model.TunnelStatusRunning}
	saveServiceFixtures(t, db, &remaining)

	tunnels := repository.NewTunnelRepository(db)
	requireServiceError(t, tunnels.RecordRelayTraffic(owner.ID, 100, 250, 100, 250, 10), nil)
	requireServiceError(t, tunnels.Delete(owner.ID), nil)

	sync := NewRuleSyncService(db)
	sample := func(created int64, input, output uint64) {
		t.Helper()
		current, err := tunnels.FindByNodeID(exit.ID)
		requireServiceError(t, err, nil)
		sync.recordRelayTraffic(exit.ID, current, []gost.ServiceConfig{{
			Name: serviceName, Handler: &gost.HandlerConfig{Type: "relay"}, Status: &gost.ServiceStatus{
				CreateTime: created, State: "running", Stats: &gost.ServiceTrafficStats{InputBytes: input, OutputBytes: output},
			},
		}})
	}
	sample(10, 100, 250)
	sample(10, 140, 300)
	sample(10, 140, 300)
	sample(10, 0, 0)
	sample(10, 10, 20)
	sample(11, 3, 4)
	requireServiceError(t, repository.NewRuleRepository(db).Update(&endpoint), nil)

	stats, err := NewStatsService(db).GetDashboardStats()
	requireServiceError(t, err, nil)
	got := stats.Traffic.Exits
	want := repository.TrafficTotals{InputBytes: 153, OutputBytes: 324}
	if len(got) != 1 || !got[0].Measured || !got[0].Running ||
		got[0].TrafficTotals != want || stats.Traffic.TrafficTotals != want {
		t.Fatalf("exit counters = %+v; total = %+v", got, stats.Traffic.TrafficTotals)
	}
}
