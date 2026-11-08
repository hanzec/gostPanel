package service

import (
	"encoding/json"
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
		sync.syncNodeRules(exit, model.GostAllowlist{}, nil)
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
	requireServiceError(t, repository.NewTunnelRepository(db).Update(&running), nil)
	requireServiceError(t, db.First(&running, running.ID).Error, nil)
	if running.RelayTraffic.InputBytes != 410 || running.RelayTraffic.LastInputBytes != 30 {
		t.Fatalf("saved relay checkpoint = %+v", running.RelayTraffic)
	}
}
