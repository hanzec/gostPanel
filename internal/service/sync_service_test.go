package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/streamtest"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestRuleSyncRetargetsRunningTunnel(t *testing.T) {
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.GostNode{}, &model.GostTunnel{}, &model.GostRule{}); err != nil {
		t.Fatal(err)
	}

	const original = `{"name":"tunnel-1-chain","metadata":{"retries":2},"hops":[{"name":"hop-0","selector":{"strategy":"fifo"},"nodes":[{"name":"exit-relay","addr":"[2001:db8::8]:8443","connector":{"type":"relay","metadata":{"nodelay":true}},"dialer":{"type":"tls","tls":{"serverName":"relay.example"}}}]}]}`
	updated := strings.Replace(original, "[2001:db8::8]:8443", "[2001:db8::9]:8443", 1)
	api := &tunnelSyncNode{t: t, chain: json.RawMessage(original)}
	entry := startServiceTestNode(t, api)
	exit := model.GostNode{Name: "exit", Address: "2001:db8::8", Port: 39000, IPv6: "2001:db8::8"}
	for _, node := range []*model.GostNode{&entry, &exit} {
		if err := db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The panel may mark the tunnel stopped when the exit goes offline, even though its chain still exists.
	tunnels := []model.GostTunnel{
		{ID: 1, Name: "live chain", EntryNodeID: entry.ID, ExitNodeID: exit.ID, RelayPort: 8449, ChainID: "tunnel-1-chain", Status: model.TunnelStatusStopped},
		{ID: 2, Name: "absent chain", EntryNodeID: entry.ID, ExitNodeID: exit.ID, RelayPort: 8444, ChainID: "tunnel-2-chain", Status: model.TunnelStatusStopped},
	}
	if err := db.Create(&tunnels).Error; err != nil {
		t.Fatal(err)
	}
	nodes := repository.NewNodeRepository(db)
	if err := nodes.UpdateIPs(context.Background(), &exit, "", "2001:db8::9"); err != nil {
		t.Fatal(err)
	}
	updatedExit, err := nodes.FindByID(exit.ID)
	if err != nil {
		t.Fatal(err)
	}
	exitAddresses := map[uint]string{updatedExit.ID: updatedExit.Address}
	service := NewRuleSyncService(db)
	assertChain := func(want string, updates, saves int) {
		t.Helper()
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.updates != updates || api.saves != saves {
			t.Errorf("chain PUTs and config saves = %d, %d; want %d, %d", api.updates, api.saves, updates, saves)
		}
		var gotConfig, wantConfig any
		if err := json.Unmarshal(api.chain, &gotConfig); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantConfig); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotConfig, wantConfig) {
			t.Errorf("chain = %s; want %s", api.chain, want)
		}
	}
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(original, 1, 0)
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(updated, 2, 1)
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(updated, 2, 2)
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(updated, 2, 2)
	for i, want := range []model.TunnelStatus{model.TunnelStatusRunning, model.TunnelStatusStopped} {
		var stored model.GostTunnel
		if err := db.First(&stored, tunnels[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != want {
			t.Errorf("tunnel %d status = %q; want %q", stored.ID, stored.Status, want)
		}
	}
}

func TestRuleSyncDoesNotOverlapCommandsForOneNode(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &blockingConfigNode{started: make(chan struct{}), release: make(chan struct{})}
	node := startServiceTestNode(t, remote)
	saveServiceFixtures(t, db, &node)
	service := NewRuleSyncService(db)
	release := sync.OnceFunc(func() { close(remote.release) })
	defer release()

	done := make(chan struct{})
	go func() {
		service.syncNodeRules(node, model.GostAllowlist{}, nil)
		close(done)
	}()
	select {
	case <-remote.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first reconciliation did not request node configuration")
	}
	service.syncNodeRules(node, model.GostAllowlist{}, nil)
	if calls := remote.calls.Load(); calls != 1 {
		t.Fatalf("overlapping reconciliation sent %d config requests", calls)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first reconciliation did not finish")
	}
	service.syncNodeRules(node, model.GostAllowlist{}, nil)
	if calls := remote.calls.Load(); calls != 2 {
		t.Fatalf("next reconciliation sent %d config requests in total", calls)
	}
}

type blockingConfigNode struct {
	nodegrpc.UnimplementedV1Server
	started, release chan struct{}
	calls            atomic.Int32
}

func (n *blockingConfigNode) Execute(ctx context.Context, request *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if request.GetMethod() != http.MethodGet || request.GetPath() != "/config" {
		return nil, status.Error(codes.InvalidArgument, "expected a Gost config request")
	}
	if n.calls.Add(1) == 1 {
		close(n.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-n.release:
		}
	}
	return &nodegrpc.CommandReply{StatusCode: http.StatusOK, Body: []byte("{}")}, nil
}

type tunnelSyncNode struct {
	nodegrpc.UnimplementedV1Server
	t       *testing.T
	mu      sync.Mutex
	chain   json.RawMessage
	updates int
	saves   int
}

func (s *tunnelSyncNode) Execute(_ context.Context, request *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	response := &nodegrpc.CommandReply{StatusCode: http.StatusOK}
	switch request.GetMethod() + " " + request.GetPath() {
	case "GET /config":
		response.Body = fmt.Appendf(nil, `{"chains":[%s]}`, s.chain)
	case "GET /config/chains/tunnel-1-chain":
		response.Body = fmt.Appendf(nil, `{"data":%s}`, s.chain)
	case "PUT /config/chains/tunnel-1-chain":
		s.updates++
		if s.updates == 1 {
			response.StatusCode = http.StatusServiceUnavailable
			break
		}
		s.chain = request.GetBody()
	case "POST /config?format=yaml":
		s.saves++
		if s.saves == 1 {
			response.StatusCode = http.StatusServiceUnavailable
		}
	default:
		s.t.Errorf("unexpected Gost command %s %s", request.GetMethod(), request.GetPath())
		return nil, status.Errorf(codes.InvalidArgument, "unexpected Gost command %s %s", request.GetMethod(), request.GetPath())
	}
	return response, nil
}

var fixtureNodeID atomic.Uint64

func startServiceTestNode(t *testing.T, server nodegrpc.V1Server) model.GostNode {
	t.Helper()
	node := model.GostNode{
		ID: uint(fixtureNodeID.Add(1)), Name: "entry", Address: "127.0.0.1", Status: model.NodeStatusOnline,
		Username: "entry", Password: "fixture-password",
	}
	session := streamtest.Open(t, nodegrpc.NewDispatcher(server,
		nodegrpc.V1_Execute_FullMethodName, nodegrpc.V1_GetObserverEndpoint_FullMethodName, nodegrpc.V1_SyncTrafficQuota_FullMethodName,
		nodegrpc.V1_GetUpgradeStatus_FullMethodName, nodegrpc.V1_StartUpgrade_FullMethodName))
	t.Cleanup(gost.RegisterNodeStream(node.ID, node.SessionGeneration, node.Username, node.Password, session))
	return node
}
