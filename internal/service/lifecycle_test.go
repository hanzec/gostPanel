package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"yagp/internal/config"
	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	testAdmin = "test-admin"
	testIP    = "192.0.2.1"
	testAgent = "service-test"
)

func TestForwardRuleLifecycle(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	remote := &lifecycleNode{t: t}
	node := startServiceTestNode(t, remote)
	saveServiceFixtures(t, db, &node)
	rules := NewRuleService(db)
	req := dto.CreateRuleReq{
		NodeID: &node.ID, Name: "primary forwarding", Type: "forward", Protocol: "tcp",
		ListenPort: 8080, Targets: []string{"target.test:443"},
	}

	invalid := req
	invalid.Type = "unknown"
	_, err := rules.Create(&invalid, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleTypeInvalid)
	invalid = req
	invalid.NodeID = nil
	_, err = rules.Create(&invalid, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrNodeRequired)
	missing := uint(99)
	invalid.NodeID = &missing
	_, err = rules.Create(&invalid, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrNodeNotFound)
	invalid = req
	invalid.ListenIP = "127.0.0.1:443"
	_, err = rules.Create(&invalid, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleListenIPInvalid)

	rule, err := rules.Create(&req, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	_, err = rules.Create(&req, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRulePortExists)
	req.Name, req.ListenPort = "other forwarding", 8081
	_, err = rules.Create(&req, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)

	update := &dto.UpdateRuleReq{Name: "primary forwarding updated", Protocol: "tcp", ListenPort: 8081, Targets: []string{"new.test:8443"}, Strategy: "fifo"}
	_, err = rules.Update(rule.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRulePortExists)
	update.ListenPort = 8082
	listenIP := " 2001:0db8::4 "
	update.ListenIP = &listenIP
	rule, err = rules.Update(rule.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if rule.Name != update.Name || rule.ListenIP != "2001:db8::4" || rule.ListenPort != update.ListenPort || !reflect.DeepEqual(rule.Targets, update.Targets) {
		t.Fatalf("rule after update = %+v", rule)
	}
	requireServiceError(t, rules.Stop(rule.ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), nil)
	running, err := rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if running.Status != model.RuleStatusRunning || running.ObserverID != "observer-global" {
		t.Fatalf("running rule = %+v", running)
	}
	service := remote.service(running.ServiceID)
	if service.Name == "" || service.Addr != "[2001:db8::4]:8082" || service.Listener.Type != "tcp" ||
		service.Handler.Type != "forward" || service.Observer != running.ObserverID ||
		service.Metadata["enableStats"] != true || service.Metadata["observer.resetTraffic"] != false ||
		len(service.Forwarder.Nodes) != 1 || service.Forwarder.Nodes[0].Addr != "new.test:8443" {
		t.Fatalf("node forwarding config = %+v", service)
	}
	_, err = rules.Update(rule.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleRunning)
	list, total, err := rules.List(&dto.RuleListReq{NodeID: node.ID, Type: "forward", Status: "running", Keyword: "primary"})
	requireServiceError(t, err, nil)
	if total != 1 || len(list) != 1 || list[0].ID != rule.ID {
		t.Fatalf("filtered rules = %+v, count %d", list, total)
	}

	requireServiceError(t, rules.Delete(rule.ID, testAdmin, testIP, testAgent), nil)
	if service := remote.service(running.ServiceID); service.Name != "" {
		t.Fatalf("deleting a running rule left its service: %+v", service)
	}
	_, err = rules.GetByID(rule.ID)
	requireServiceError(t, err, bizerrors.ErrRuleNotFound)
	_, err = rules.Update(rule.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleNotFound)
	requireServiceError(t, rules.Delete(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleNotFound)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), gorm.ErrRecordNotFound)
	requireServiceError(t, rules.Stop(rule.ID, testAdmin, testIP, testAgent), gorm.ErrRecordNotFound)

	logs, count, err := NewLogService(db).List(&dto.LogListReq{Action: model.ActionDelete, ResourceType: model.ResourceTypeRule, Username: "test-ad"})
	requireServiceError(t, err, nil)
	if count != 1 || len(logs) != 1 || logs[0].ResourceID != rule.ID || logs[0].IPAddress != testIP || logs[0].UserAgent != testAgent {
		t.Fatalf("deletion audit log = %+v", logs)
	}
}

func TestTunnelAndRuleLifecycle(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry := startServiceTestNode(t, entryRemote)
	exit := startServiceTestNode(t, exitRemote)
	exit.Name, entry.IPv4 = "exit", "192.0.2.8"
	saveServiceFixtures(t, db, &entry, &exit)
	tunnels, rules := NewTunnelService(db), NewRuleService(db)
	req := &dto.CreateTunnelReq{Name: "relay path", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 8443}
	for _, tc := range []struct {
		entry uint
		exit  uint
		err   error
	}{
		{entry.ID, entry.ID, bizerrors.ErrTunnelNodeSame},
		{99, exit.ID, bizerrors.ErrEntryNodeNotFound},
		{entry.ID, 99, bizerrors.ErrExitNodeNotFound},
	} {
		invalid := *req
		invalid.EntryNodeID, invalid.ExitNodeID = tc.entry, tc.exit
		_, err := tunnels.Create(&invalid, testAdmin, testIP, testAgent)
		requireServiceError(t, err, tc.err)
	}
	tunnel, err := tunnels.Create(req, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	update := &dto.UpdateTunnelReq{Name: "relay path updated", Protocol: "udp", RelayPort: 8444, Remark: "UDP relay"}
	tunnel, err = tunnels.Update(tunnel.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if tunnel.Name != update.Name || tunnel.Protocol != "udp" || tunnel.RelayPort != 8444 {
		t.Fatalf("updated tunnel = %+v", tunnel)
	}

	ruleReq := &dto.CreateRuleReq{Name: "relayed forwarding", Type: "tunnel", Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: 5353, Targets: []string{"resolver.test:53"}}
	_, err = rules.Create(ruleReq, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelRequired)
	missing := uint(99)
	ruleReq.TunnelID = &missing
	_, err = rules.Create(ruleReq, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelNotFound)
	ruleReq.TunnelID = &tunnel.ID
	rule, err := rules.Create(ruleReq, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelNotRunning)
	requireServiceError(t, tunnels.Delete(tunnel.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelHasRules)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	_, err = tunnels.UpdateAllowlist(context.Background(), tunnel.ID, []uint{entry.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)

	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	running, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if running.Status != model.TunnelStatusRunning || running.ChainID == "" || running.ServiceID == "" {
		t.Fatalf("running tunnel = %+v", running)
	}
	chainID, err := tunnels.GetChainID(tunnel.ID)
	requireServiceError(t, err, nil)
	chain := entryRemote.chain(chainID)
	if len(chain.Hops) != 1 || len(chain.Hops[0].Nodes) != 1 {
		t.Fatalf("entry chain = %+v", chain)
	}
	relayNode := chain.Hops[0].Nodes[0]
	if relayNode.Addr != "127.0.0.1:8444" || relayNode.Dialer.Type != "udp" || relayNode.Connector.Type != "relay" {
		t.Fatalf("relay chain target = %+v", relayNode)
	}
	relay := exitRemote.service(running.ServiceID)
	if relay.Name == "" || relay.Handler.Type != "relay" || relay.Listener.Type != "udp" || relay.Admission != gost.TunnelRelayAllowlistName(relay.Name) {
		t.Fatalf("exit relay = %+v", relay)
	}
	allowlist := exitRemote.admission(relay.Admission)
	if !allowlist.Whitelist || !reflect.DeepEqual(allowlist.Matchers, []string{entry.IPv4}) {
		t.Fatalf("relay allowlist = %+v", allowlist)
	}
	_, err = tunnels.Update(tunnel.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelRunning)
	tunnelList, total, err := tunnels.List(&dto.TunnelListReq{NodeID: exit.ID, Status: "running", Keyword: "relay"})
	requireServiceError(t, err, nil)
	if total != 1 || len(tunnelList) != 1 || tunnelList[0].ID != tunnel.ID {
		t.Fatalf("filtered tunnels = %+v, total %d", tunnelList, total)
	}

	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), nil)
	ruleList, total, err := rules.List(&dto.RuleListReq{TunnelID: tunnel.ID, Type: "tunnel"})
	requireServiceError(t, err, nil)
	if total != 1 || len(ruleList) != 1 || ruleList[0].Status != model.RuleStatusRunning {
		t.Fatalf("running tunneled rules = %+v, total %d", ruleList, total)
	}
	forwarding := entryRemote.service(ruleList[0].ServiceID)
	if forwarding.Name == "" || forwarding.Addr != "127.0.0.1:5353" || forwarding.Listener.Type != "udp" || forwarding.Handler.Chain != chainID {
		t.Fatalf("tunneled forwarding service = %+v", forwarding)
	}
	stats, err := NewStatsService(db).GetDashboardStats()
	requireServiceError(t, err, nil)
	if stats.Nodes != (NodeStats{Total: 2, Online: 2}) ||
		stats.Rules != (RuleStats{Total: 1, Running: 1, TunnelType: 1}) ||
		stats.Tunnels != (TunnelStats{Total: 1, Running: 1}) || stats.Database != "sqlite" {
		t.Fatalf("dashboard statistics = %+v", stats)
	}
	requireServiceError(t, rules.Delete(rule.ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, tunnels.Delete(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if entryRemote.chain(chainID).Name != "" || exitRemote.service(running.ServiceID).Name != "" {
		t.Fatal("deleting running tunnel did not remove both endpoints")
	}
	_, err = tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, bizerrors.ErrTunnelNotFound)
	_, err = tunnels.Update(tunnel.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelNotFound)
	_, err = tunnels.GetChainID(tunnel.ID)
	requireServiceError(t, err, gorm.ErrRecordNotFound)
	requireServiceError(t, tunnels.Delete(tunnel.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelNotFound)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), gorm.ErrRecordNotFound)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), gorm.ErrRecordNotFound)
}

func TestTunnelStartRollsBackRelayAfterChainFailure(t *testing.T) {
	db := newServiceTestDB(t)
	entryRemote := &lifecycleNode{t: t, reject: map[string]int{"POST /config/chains": http.StatusServiceUnavailable}}
	exitRemote := &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	tunnels := NewTunnelService(db)
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{Name: "relay", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 8443}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelChainCreateFailed)
	stored, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if stored.Status != model.TunnelStatusError || exitRemote.service(relayServiceName(tunnel.ID)).Name != "" {
		t.Fatalf("failed start left a relay or wrong status: %+v", stored)
	}
}

func TestTunnelStartRollsBackRelayUntilExitReportsAnAddress(t *testing.T) {
	db := newServiceTestDB(t)
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name, exit.Address = "exit", ""
	saveServiceFixtures(t, db, &entry, &exit)
	service := NewTunnelService(db)
	tunnel, err := service.Create(&dto.CreateTunnelReq{
		Name: "relay", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 8443,
	}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, service.Start(tunnel.ID, testAdmin, testIP, testAgent), bizerrors.ErrExtractHostFailed)
	stored, err := service.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if stored.Status != model.TunnelStatusError || exitRemote.service(relayServiceName(tunnel.ID)).Name != "" ||
		entryRemote.chain(tunnelChainName(tunnel.ID)).Name != "" {
		t.Fatal("missing exit address left live tunnel resources")
	}
	requireServiceError(t, NewNodeService(db, "").UpdateIPs(t.Context(), &exit, "192.0.2.85", ""), nil)
	requireServiceError(t, service.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	stored, err = service.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	addr, ok := entryRemote.chain(tunnelChainName(tunnel.ID)).TunnelRelayAddress()
	if stored.Status != model.TunnelStatusRunning || !ok || addr != "192.0.2.85:8443" {
		t.Fatalf("retry after IP report = status %s, relay address %q", stored.Status, addr)
	}
}

func newServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	requireServiceError(t, err, nil)
	sqlDB, err := db.DB()
	requireServiceError(t, err, nil)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	requireServiceError(t, db.AutoMigrate(&model.GostNode{}, &model.GostRule{}, &model.GostTunnel{}, &model.OperationLog{}, &model.SystemConfig{}, &model.GostRuleset{}), nil)
	return db
}

func loadServiceConfig(t *testing.T, contents string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	requireServiceError(t, os.WriteFile(path, []byte(contents), 0o600), nil)
	cfg, err := config.Load(path)
	requireServiceError(t, err, nil)
	return cfg
}

func saveServiceFixtures(t *testing.T, db *gorm.DB, rows ...any) {
	t.Helper()
	for _, row := range rows {
		requireServiceError(t, db.Create(row).Error, nil)
	}
}

func requireServiceError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

// lifecycleNode serves Gost resource responses over a real node WebSocket session.
type lifecycleNode struct {
	nodegrpc.UnimplementedV1Server
	t           *testing.T
	mu          sync.Mutex
	resources   map[string]json.RawMessage
	reject      map[string]int
	gostConfig  string
	observerErr error
}

func (n *lifecycleNode) Execute(_ context.Context, req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	method, path := req.GetMethod(), req.GetPath()
	if code := n.reject[method+" "+path]; code != 0 {
		return &nodegrpc.CommandReply{StatusCode: int32(code)}, nil
	}
	if path == "/config" && method == http.MethodGet {
		body := n.gostConfig
		if body == "" {
			body = "{}"
		}
		return &nodegrpc.CommandReply{StatusCode: http.StatusOK, Body: []byte(body)}, nil
	}
	if path == "/config?format=yaml" && method == http.MethodPost {
		return &nodegrpc.CommandReply{StatusCode: http.StatusOK}, nil
	}
	if n.resources == nil {
		n.resources = make(map[string]json.RawMessage)
	}
	switch method {
	case http.MethodGet:
		resource := n.resources[path]
		if resource == nil {
			resource = []byte("null")
		}
		return &nodegrpc.CommandReply{StatusCode: http.StatusOK, Body: fmt.Appendf(nil, "{\"data\":%s}", resource)}, nil
	case http.MethodPost:
		var record struct{ Name string }
		if err := json.Unmarshal(req.GetBody(), &record); err != nil || record.Name == "" {
			n.t.Errorf("invalid node resource at %s: %s (%v)", path, req.GetBody(), err)
			return &nodegrpc.CommandReply{StatusCode: http.StatusBadRequest}, nil
		}
		n.resources[path+"/"+record.Name] = req.GetBody()
		return &nodegrpc.CommandReply{StatusCode: http.StatusCreated}, nil
	case http.MethodPut:
		n.resources[path] = req.GetBody()
		return &nodegrpc.CommandReply{StatusCode: http.StatusOK}, nil
	case http.MethodDelete:
		delete(n.resources, path)
		return &nodegrpc.CommandReply{StatusCode: http.StatusNoContent}, nil
	default:
		n.t.Errorf("unexpected node command %s %s", method, path)
		return nil, status.Error(codes.InvalidArgument, "unexpected node command")
	}
}

func (n *lifecycleNode) GetObserverEndpoint(context.Context, *emptypb.Empty) (*nodegrpc.ObserverEndpoint, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.observerErr != nil {
		return nil, n.observerErr
	}
	return &nodegrpc.ObserverEndpoint{Address: "127.0.0.1:39001"}, nil
}

func (n *lifecycleNode) service(name string) (result gost.ServiceConfig) {
	n.read("/config/services/"+name, &result)
	return result
}

func (n *lifecycleNode) chain(name string) (result gost.ChainConfig) {
	n.read("/config/chains/"+name, &result)
	return result
}

func (n *lifecycleNode) admission(name string) (result gost.AdmissionConfig) {
	n.read("/config/admissions/"+name, &result)
	return result
}

func (n *lifecycleNode) read(path string, dest any) {
	n.t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if raw, ok := n.resources[path]; ok {
		requireServiceError(n.t, json.Unmarshal(raw, dest), nil)
	}
}
