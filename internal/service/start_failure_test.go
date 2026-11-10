package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestRuleStartHandlesNodeObserverAndProvisioningFailures(t *testing.T) {
	db := newServiceTestDB(t)
	cfg := loadServiceConfig(t, "{}\n")
	remote := &lifecycleNode{t: t}
	node := startServiceTestNode(t, remote)
	saveServiceFixtures(t, db, &node)
	rules := NewRuleService(db)
	rule, err := rules.Create(&dto.CreateRuleReq{NodeID: &node.ID, Name: "forward", Type: "forward", Protocol: "tcp", ListenPort: 8080, Targets: []string{"target.test:80"}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	start := func(want error) {
		t.Helper()
		requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), want)
	}
	start(bizerrors.ErrReportNotConfigured)
	cfg.Report.Listen = "127.0.0.1:39001"
	updateServiceFields(t, db, &node, map[string]any{"status": model.NodeStatusOffline})
	start(bizerrors.ErrNodeOffline)
	updateServiceFields(t, db, rule, map[string]any{"status": model.RuleStatusRunning})
	requireServiceError(t, rules.Stop(rule.ID, testAdmin, testIP, testAgent), nil)
	updateServiceFields(t, db, &node, map[string]any{"status": model.NodeStatusOnline})

	remote.mu.Lock()
	remote.observerErr = status.Error(codes.Unavailable, "observer endpoint unavailable")
	remote.mu.Unlock()
	start(bizerrors.ErrObserverCreateFailed)
	remote.mu.Lock()
	remote.observerErr = nil
	remote.mu.Unlock()
	remote.rejectCommand("POST /config/observers", http.StatusServiceUnavailable)
	start(bizerrors.ErrObserverCreateFailed)
	remote.rejectCommand("POST /config/observers", 0)

	requireServiceError(t, db.Exec("CREATE TRIGGER reject_observer BEFORE UPDATE OF observer_id ON rules BEGIN SELECT RAISE(ABORT, 'test write rejected'); END").Error, nil)
	requireWriteRejected(t, rules.Start(rule.ID, testAdmin, testIP, testAgent))
	requireServiceError(t, db.Exec("DROP TRIGGER reject_observer").Error, nil)
	remote.rejectCommand("POST /config/services", http.StatusServiceUnavailable)
	start(bizerrors.ErrRuleStartFailed)
	remote.rejectCommand("POST /config/services", 0)

	updateServiceFields(t, db, &node, map[string]any{"traffic_limit_bytes": 100})
	start(bizerrors.ErrNodeTrafficQuotaUnavailable)
	updateServiceFields(t, db, &node, map[string]any{"traffic_limit_bytes": 0})
	if remote.service(fmt.Sprintf("rule-%d", rule.ID)).Name != "" {
		t.Fatal("failed rule start left a forwarding service")
	}

	start(nil)
	running, err := rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	remote.rejectCommand("DELETE /config/services/"+running.ServiceID, http.StatusServiceUnavailable)
	requireServiceError(t, rules.Stop(rule.ID, testAdmin, testIP, testAgent), nil)
	saved, err := rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if saved.Status != model.RuleStatusStopped {
		t.Fatalf("stop with unavailable node API persisted %q", saved.Status)
	}
	updateServiceFields(t, db, rule, map[string]any{"node_id": 99})
	start(bizerrors.ErrNodeNotFound)
	updateServiceFields(t, db, rule, map[string]any{"status": model.RuleStatusRunning})
	requireServiceError(t, rules.Stop(rule.ID, testAdmin, testIP, testAgent), nil)
}

func TestTunnelRuleRejectsMissingTunnelOrChain(t *testing.T) {
	db := newServiceTestDB(t)
	entry := model.GostNode{Name: "entry", Port: 39000, Status: model.NodeStatusOnline}
	exit := model.GostNode{Name: "exit", Port: 39000, Status: model.NodeStatusOnline}
	saveServiceFixtures(t, db, &entry, &exit)
	tunnel := model.GostTunnel{Name: "relay", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Status: model.TunnelStatusRunning}
	saveServiceFixtures(t, db, &tunnel)
	rules := NewRuleService(db)
	rule := model.GostRule{Name: "tunneled", Type: model.RuleTypeTunnel, NodeID: &entry.ID, ListenPort: 8080}
	saveServiceFixtures(t, db, &rule)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelRequired)
	updateServiceFields(t, db, &rule, map[string]any{"tunnel_id": 99})
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelNotFound)
	updateServiceFields(t, db, &rule, map[string]any{"tunnel_id": tunnel.ID})
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelChainNotFound)
	saved, err := rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if saved.Status != model.RuleStatusError {
		t.Fatalf("missing-chain rule status = %q", saved.Status)
	}
}

func TestTunnelProvisioningRejectsUnavailableEndpoints(t *testing.T) {
	ctx := context.Background()
	db := newServiceTestDB(t)
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry := startServiceTestNode(t, entryRemote)
	exit := startServiceTestNode(t, exitRemote)
	exit.Name, entry.IPv4 = "exit", "192.0.2.3"
	saveServiceFixtures(t, db, &entry, &exit)
	tunnel := model.GostTunnel{Name: "relay", EntryNodeID: entry.ID, ExitNodeID: exit.ID, RelayPort: 8443}
	saveServiceFixtures(t, db, &tunnel)
	tunnels := NewTunnelService(db)
	start := func(want error) {
		t.Helper()
		requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), want)
	}
	updateServiceFields(t, db, &tunnel, map[string]any{"entry_node_id": 99})
	start(bizerrors.ErrEntryNodeNotFound)
	updateServiceFields(t, db, &tunnel, map[string]any{"entry_node_id": entry.ID, "exit_node_id": 99})
	start(bizerrors.ErrExitNodeNotFound)
	updateServiceFields(t, db, &tunnel, map[string]any{"exit_node_id": exit.ID})
	updateServiceFields(t, db, &entry, map[string]any{"status": model.NodeStatusOffline})
	start(bizerrors.ErrEntryNodeOffline)
	updateServiceFields(t, db, &entry, map[string]any{"status": model.NodeStatusOnline})
	updateServiceFields(t, db, &exit, map[string]any{"status": model.NodeStatusOffline})
	start(bizerrors.ErrExitNodeOffline)
	updateServiceFields(t, db, &exit, map[string]any{"status": model.NodeStatusOnline})

	_, err := tunnels.UpdateAllowlist(ctx, tunnel.ID, []uint{entry.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	for _, command := range []string{"GET /config", "POST /config/admissions"} {
		exitRemote.rejectCommand(command, http.StatusServiceUnavailable)
		if err := tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent); err == nil {
			t.Fatalf("tunnel started despite failing %s", command)
		}
		exitRemote.rejectCommand(command, 0)
	}
	exitRemote.rejectCommand("POST /config/services", http.StatusServiceUnavailable)
	start(bizerrors.ErrTunnelRelayCreateFailed)
	exitRemote.rejectCommand("POST /config/services", 0)
	updateServiceFields(t, db, &exit, map[string]any{"traffic_limit_bytes": 100})
	start(bizerrors.ErrNodeTrafficQuotaUnavailable)
	updateServiceFields(t, db, &exit, map[string]any{"traffic_limit_bytes": 0})

	relay := gost.ServiceConfig{Name: relayServiceName(tunnel.ID), Handler: &gost.HandlerConfig{Type: "relay"}}
	jsonRelay, err := json.Marshal(relay)
	requireServiceError(t, err, nil)
	exitRemote.mu.Lock()
	exitRemote.resources["/config/services/"+relay.Name] = jsonRelay
	exitRemote.mu.Unlock()
	exitRemote.rejectCommand("PUT /config/services/"+relay.Name, http.StatusServiceUnavailable)
	if err := tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent); err == nil {
		t.Fatal("tunnel started despite failing to attach the allowlist")
	}
	exitRemote.rejectCommand("PUT /config/services/"+relay.Name, 0)
	start(nil)
	entryRemote.rejectCommand("DELETE /config/chains/"+tunnelChainName(tunnel.ID), http.StatusServiceUnavailable)
	exitRemote.rejectCommand("DELETE /config/services/"+relay.Name, http.StatusServiceUnavailable)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	saved, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if saved.Status != model.TunnelStatusStopped {
		t.Fatalf("stop with unavailable node APIs persisted %q", saved.Status)
	}
}

func updateServiceFields(t *testing.T, db *gorm.DB, row any, fields map[string]any) {
	t.Helper()
	requireServiceError(t, db.Model(row).Updates(fields).Error, nil)
}

func (n *lifecycleNode) rejectCommand(command string, code int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.reject == nil {
		n.reject = make(map[string]int)
	}
	n.reject[command] = code
}
