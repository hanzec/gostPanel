package service

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestTrafficQuotaSync(t *testing.T) {
	db := newTrafficQuotaTestDB(t)
	repo := repository.NewNodeRepository(db)
	fixture := &trafficQuotaTestNode{}
	node := startServiceTestNode(t, fixture)
	node.TrafficLimitBytes, node.TotalBytes = 100, 40
	node.TrafficQuotaState = model.TrafficQuotaPending
	if err := repo.Create(&node); err != nil {
		t.Fatal(err)
	}
	client := utils.GetGostClient(&node)
	services := []gost.ServiceConfig{
		{Name: "rule-1", Addr: "127.0.0.1:5010", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
		{Name: "rule-2", Addr: "[2001:db8::4]:5011", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "udp"}},
		{Name: "relay-tunnel-3", Addr: "[::]:5012", Handler: &gost.HandlerConfig{Type: "relay"}, Listener: &gost.ListenerConfig{Type: "quic"}},
		{Name: "other-service", Addr: ":5013", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
		{Name: "rule-4", Addr: ":5010", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
		{Name: "rule-5", Addr: ":5014", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}, Status: &gost.ServiceStatus{State: "failed"}},
		{Name: "rule-6", Addr: ":0", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
		{Name: "rule-7", Addr: "invalid", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
		{Name: "rule-8", Addr: "127.0.0.1:443", Handler: &gost.HandlerConfig{Type: "red"}, Listener: &gost.ListenerConfig{Type: "red"}},
	}
	now := time.Date(2026, 11, 8, 15, 59, 59, 0, time.UTC)
	sync := func(limit, seed, used uint64, window int64, state string) {
		t.Helper()
		fixture.mu.Lock()
		fixture.check = func(req *nodegrpc.TrafficQuotaRequest) {
			if req.GetLimitBytes() != limit || req.GetUsedBytes() != seed || req.GetWindowStart() != window {
				t.Errorf("quota request = %v, want limit %d, seed %d, window %d", req, limit, seed, window)
			}
			if limit > 0 && (!slices.Equal(req.GetTcpPorts(), []uint32{443, 5010}) || !slices.Equal(req.GetUdpPorts(), []uint32{5011, 5012})) {
				t.Errorf("quota ports = TCP %v UDP %v", req.GetTcpPorts(), req.GetUdpPorts())
			}
			if limit == 0 && (len(req.GetTcpPorts()) != 0 || len(req.GetUdpPorts()) != 0) {
				t.Errorf("clearing quota sent ports: %v", req)
			}
		}
		fixture.used = used
		fixture.mu.Unlock()
		if err := syncTrafficQuota(repo, client, node, services, now); err != nil {
			t.Fatal(err)
		}
		saved, err := repo.FindByID(node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.TrafficQuotaUsedBytes != used || saved.TrafficQuotaState != state || saved.TrafficQuotaWindowStart != window || saved.TotalBytes != 40 {
			t.Fatalf("quota snapshot = %d bytes %q at %d, historical total %d", saved.TrafficQuotaUsedBytes, saved.TrafficQuotaState, saved.TrafficQuotaWindowStart, saved.TotalBytes)
		}
		node = *saved
	}
	sync(100, 40, 100, 0, model.TrafficQuotaBlocked)
	daily := "daily"
	if err := repo.Update(&node, nil, &daily); err != nil {
		t.Fatal(err)
	}
	sync(100, 0, 100, model.TrafficQuotaWindow(daily, now), model.TrafficQuotaBlocked)
	now = now.Add(time.Second)
	sync(100, 0, 2, model.TrafficQuotaWindow(daily, now), model.TrafficQuotaActive)
	sync(100, 2, 12, model.TrafficQuotaWindow(daily, now), model.TrafficQuotaActive)
	zero := int64(0)
	if err := repo.Update(&node, &zero, nil); err != nil {
		t.Fatal(err)
	}
	sync(0, 0, 0, model.TrafficQuotaWindowUnset, "")
}

func TestCreateServiceWithTrafficQuotaRollsBackOnlyNewService(t *testing.T) {
	const lookup = "GET /config/services/rule-1"
	const add = "POST /config/services"
	const remove = "DELETE /config/services/rule-1"
	absent := trafficQuotaAdminCommand{lookup, http.StatusOK, `{"data":null}`}
	present := trafficQuotaAdminCommand{lookup, http.StatusOK, `{"data":{"name":"rule-1"}}`}
	created := trafficQuotaAdminCommand{add, http.StatusCreated, ""}
	configured := trafficQuotaAdminCommand{"GET /config", http.StatusOK, `{"services":[{"name":"rule-1","addr":":5010","handler":{"type":"forward"},"listener":{"type":"tcp"}}]}`}
	rejected := trafficQuotaAdminCommand{add, http.StatusConflict, "address already in use"}
	deleted := trafficQuotaAdminCommand{remove, http.StatusNoContent, ""}
	for _, tc := range []struct {
		name           string
		limit          int64
		quotaCode      codes.Code
		commands       []trafficQuotaAdminCommand
		wantState      string
		wantErr        error
		wantErrMessage string
	}{
		{"quota applied after creation", 100, codes.OK, []trafficQuotaAdminCommand{absent, created, configured}, model.TrafficQuotaActive, nil, ""},
		{"quota failure deletes newly created service", 100, codes.Unavailable, []trafficQuotaAdminCommand{absent, created, configured, present, deleted}, model.TrafficQuotaPending, bizerrors.ErrNodeTrafficQuotaUnavailable, ""},
		{"unsupported quota preserves preexisting service", 100, codes.Unimplemented, []trafficQuotaAdminCommand{present, configured}, model.TrafficQuotaUnsupported, bizerrors.ErrNodeTrafficQuotaUnavailable, ""},
		{"rejected service creation skips quota", 100, codes.OK, []trafficQuotaAdminCommand{absent, rejected}, "", nil, "address already in use"},
		{"uncapped node skips quota", 0, codes.OK, []trafficQuotaAdminCommand{absent, created}, "", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := repository.NewNodeRepository(newTrafficQuotaTestDB(t))
			fixture := &trafficQuotaAdminTestNode{t: t, commands: slices.Clone(tc.commands)}
			fixture.quotaErr = status.Error(tc.quotaCode, "test quota failure")
			fixture.used = 12
			fixture.check = func(req *nodegrpc.TrafficQuotaRequest) {
				if req.GetLimitBytes() != uint64(tc.limit) || !slices.Equal(req.GetTcpPorts(), []uint32{5010}) {
					t.Errorf("unexpected quota request: %v", req)
				}
			}
			node := startServiceTestNode(t, fixture)
			node.TrafficLimitBytes = tc.limit
			if err := repo.Create(&node); err != nil {
				t.Fatal(err)
			}
			service := gost.BuildTCPForwardService("rule-1", 5010, []string{"localhost:8080"}, "round")
			err := createServiceWithTrafficQuota(repo, utils.GetGostClient(&node), node, service)
			if tc.wantErrMessage != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrMessage) {
					t.Errorf("start error = %v, want error containing %q", err, tc.wantErrMessage)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Errorf("start error = %v, want %v", err, tc.wantErr)
			}
			saved, err := repo.FindByID(node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.TrafficQuotaState != tc.wantState {
				t.Errorf("saved quota state = %q, want %q", saved.TrafficQuotaState, tc.wantState)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			wantQuotaCalls := 1
			if tc.wantState == "" {
				wantQuotaCalls = 0
			}
			if len(fixture.commands) != 0 || fixture.calls != wantQuotaCalls {
				t.Errorf("remaining Gost commands: %v, quota calls: %d, want %d", fixture.commands, fixture.calls, wantQuotaCalls)
			}
		})
	}
}

func TestRuleSyncClearsQuotaEvenWhenGostConfigIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		quotaCode codes.Code
		window    int64
		used      uint64
	}{
		{"applied quota", codes.OK, 1793548800, 100},
		{"quota never applied on older daemon", codes.Unimplemented, model.TrafficQuotaWindowUnset, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTrafficQuotaTestDB(t)
			repo := repository.NewNodeRepository(db)
			fixture := &trafficQuotaAdminTestNode{
				t:        t,
				commands: []trafficQuotaAdminCommand{{"GET /config", http.StatusServiceUnavailable, ""}},
			}
			fixture.quotaErr = status.Error(tc.quotaCode, "test quota failure")
			fixture.check = func(req *nodegrpc.TrafficQuotaRequest) {
				if req.GetLimitBytes() != 0 || len(req.GetTcpPorts()) != 0 || len(req.GetUdpPorts()) != 0 {
					t.Errorf("unexpected quota clear request: %v", req)
				}
			}
			node := startServiceTestNode(t, fixture)
			node.TrafficQuotaState = model.TrafficQuotaPending
			node.TrafficQuotaUsedBytes = tc.used
			node.TrafficQuotaWindowStart = tc.window
			if err := repo.Create(&node); err != nil {
				t.Fatal(err)
			}
			NewRuleSyncService(db).syncNodeRules(node, model.GostAllowlist{}, nil)
			saved, err := repo.FindByID(node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.TrafficQuotaState != "" || saved.TrafficQuotaUsedBytes != 0 || saved.TrafficQuotaWindowStart != model.TrafficQuotaWindowUnset {
				t.Errorf("quota after clearing = %q, %d bytes, window %d", saved.TrafficQuotaState, saved.TrafficQuotaUsedBytes, saved.TrafficQuotaWindowStart)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.commands) != 0 || fixture.calls != 1 {
				t.Errorf("remaining Gost commands: %v, quota calls: %d", fixture.commands, fixture.calls)
			}
		})
	}
}

func newTrafficQuotaTestDB(t *testing.T) *gorm.DB {
	t.Helper()
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
	if err := db.AutoMigrate(&model.GostNode{}); err != nil {
		t.Fatal(err)
	}
	return db
}

type trafficQuotaTestNode struct {
	nodegrpc.UnimplementedV1Server
	mu       sync.Mutex
	check    func(*nodegrpc.TrafficQuotaRequest)
	used     uint64
	quotaErr error
	calls    int
}

func (s *trafficQuotaTestNode) SyncTrafficQuota(_ context.Context, req *nodegrpc.TrafficQuotaRequest) (*nodegrpc.TrafficQuotaReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.check(req)
	if s.quotaErr != nil {
		return nil, s.quotaErr
	}
	return &nodegrpc.TrafficQuotaReply{UsedBytes: s.used}, nil
}

type trafficQuotaAdminCommand struct {
	command string
	code    int
	body    string
}

type trafficQuotaAdminTestNode struct {
	trafficQuotaTestNode
	t        *testing.T
	commands []trafficQuotaAdminCommand
}

func (s *trafficQuotaAdminTestNode) Execute(_ context.Context, req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command := req.GetMethod() + " " + req.GetPath()
	if len(s.commands) == 0 || s.commands[0].command != command {
		s.t.Errorf("unexpected Gost command %s", command)
		return nil, status.Errorf(codes.InvalidArgument, "unexpected Gost command %s", command)
	}
	response := s.commands[0]
	s.commands = s.commands[1:]
	return &nodegrpc.CommandReply{StatusCode: int32(response.code), Body: []byte(response.body)}, nil
}
