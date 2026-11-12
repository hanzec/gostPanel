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
		if err := syncTrafficQuota(repo, client, node.ID, services, now); err != nil {
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
	if err := repo.Update(&node, nil, &daily, nil); err != nil {
		t.Fatal(err)
	}
	sync(100, 0, 100, model.TrafficQuotaWindow(daily, now), model.TrafficQuotaBlocked)
	now = now.Add(time.Second)
	sync(100, 0, 2, model.TrafficQuotaWindow(daily, now), model.TrafficQuotaActive)
	sync(100, 2, 12, model.TrafficQuotaWindow(daily, now), model.TrafficQuotaActive)
	zero := int64(0)
	if err := repo.Update(&node, &zero, nil, nil); err != nil {
		t.Fatal(err)
	}
	sync(0, 0, 0, model.TrafficQuotaWindowUnset, "")
}

func TestTrafficCalibrationSurvivesSyncWithStaleNode(t *testing.T) {
	db := newTrafficQuotaTestDB(t)
	repo := repository.NewNodeRepository(db)
	config := trafficQuotaAdminCommand{"GET /config", http.StatusOK, `{"services":[]}`}
	remote := &trafficQuotaAdminTestNode{t: t, commands: []trafficQuotaAdminCommand{config, config}}
	node := startServiceTestNode(t, remote)
	node.TrafficLimitBytes = 100
	if err := repo.Create(&node); err != nil {
		t.Fatal(err)
	}
	requireServiceError(t, repo.UpdateTrafficQuota(node, 0, 120, model.TrafficQuotaBlocked), nil)
	stale, err := repo.FindByID(node.ID)
	requireServiceError(t, err, nil)
	remote.check = func(req *nodegrpc.TrafficQuotaRequest) {
		if req.CalibratedUsedBytes == nil || req.GetCalibratedUsedBytes() != 0 || req.GetUsedBytes() != 120 {
			t.Errorf("unexpected quota calibration request: %v", req)
		}
	}
	remote.calibrated = true
	service := NewNodeService(db, "")
	requireServiceError(t, service.CalibrateNodeTotals(t.Context(), node.ID, 30, 60), nil)
	requireServiceError(t, service.CalibrateNodeQuota(t.Context(), node.ID, 0), nil)

	remote.mu.Lock()
	remote.calibrated, remote.used = false, 3
	remote.check = func(req *nodegrpc.TrafficQuotaRequest) {
		if req.CalibratedUsedBytes != nil || req.GetUsedBytes() != 0 {
			t.Errorf("sync reseeded the previous quota count: %v", req)
		}
	}
	remote.mu.Unlock()
	_, err = NewRuleSyncService(db).getNodeConfig(utils.GetGostClient(stale), *stale)
	requireServiceError(t, err, nil)
	saved, err := repo.FindByID(node.ID)
	requireServiceError(t, err, nil)
	if saved.TotalBytes != 90 || saved.TrafficQuotaUsedBytes != 3 || saved.TrafficQuotaState != model.TrafficQuotaActive {
		t.Fatalf("unexpected counters after calibration and sync: total=%d quota=%d state=%s", saved.TotalBytes, saved.TrafficQuotaUsedBytes, saved.TrafficQuotaState)
	}
}

func TestTrafficCalibrationRequiresAnEligibleNodeAndConfirmedQuota(t *testing.T) {
	db := newTrafficQuotaTestDB(t)
	repo := repository.NewNodeRepository(db)
	service := NewNodeService(db, "")
	ctx := t.Context()
	config := trafficQuotaAdminCommand{"GET /config", http.StatusOK, `{"services":[{"name":"rule-1","addr":":5010","handler":{"type":"forward"},"listener":{"type":"tcp"}}]}`}
	remote := &trafficQuotaAdminTestNode{t: t, commands: []trafficQuotaAdminCommand{
		{"GET /config", http.StatusServiceUnavailable, "Gost is restarting"}, config, config,
	}}
	remote.check = func(req *nodegrpc.TrafficQuotaRequest) {
		if req.GetLimitBytes() != 100 || req.GetWindowStart() != 0 || req.GetUsedBytes() != 0 ||
			req.GetCalibratedUsedBytes() != 100 || !slices.Equal(req.GetTcpPorts(), []uint32{5010}) {
			t.Errorf("unexpected quota calibration request: %v", req)
		}
	}
	remote.quotaErr = status.Error(codes.Unavailable, "quota command unavailable")
	remote.calibrated = true
	remote.used = 100
	node := startServiceTestNode(t, remote)
	requireServiceError(t, service.CalibrateNodeTotals(ctx, node.ID, 10, 20), bizerrors.ErrNodeNotFound)
	requireServiceError(t, service.CalibrateNodeQuota(ctx, node.ID, 100), bizerrors.ErrNodeNotFound)
	node.Status = model.NodeStatusOffline
	node.TrafficLimitBytes = 100
	node.TrafficQuotaWindowStart = 24
	node.TrafficQuotaUsedBytes = 25
	node.TrafficQuotaState = model.TrafficQuotaActive
	requireServiceError(t, repo.Create(&node), nil)
	if err := service.CalibrateNodeQuota(ctx, node.ID, 100); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("calibrating quota on an offline node: %v", err)
	}
	requireServiceError(t, repo.UpdateStatus(node.ID, model.NodeStatusOnline), nil)
	if err := service.CalibrateNodeQuota(ctx, node.ID, 100); err == nil || !strings.Contains(err.Error(), "Gost is restarting") {
		t.Fatalf("calibrating quota with a failed Gost config lookup: %v", err)
	}
	if err := service.CalibrateNodeQuota(ctx, node.ID, 100); status.Code(err) != codes.Unavailable {
		t.Fatalf("calibrating quota with a failed daemon command: %v", err)
	}
	saved, err := repo.FindByID(node.ID)
	requireServiceError(t, err, nil)
	if saved.TrafficQuotaUsedBytes != 25 || saved.TrafficQuotaWindowStart != 24 || saved.TrafficQuotaState != model.TrafficQuotaActive {
		t.Fatalf("failed calibration changed the stored quota: %+v", saved)
	}
	remote.mu.Lock()
	remote.quotaErr = nil
	remote.mu.Unlock()
	requireServiceError(t, service.CalibrateNodeQuota(ctx, node.ID, 100), nil)
	saved, err = repo.FindByID(node.ID)
	requireServiceError(t, err, nil)
	if saved.TrafficQuotaUsedBytes != 100 || saved.TrafficQuotaWindowStart != 0 || saved.TrafficQuotaState != model.TrafficQuotaBlocked {
		t.Fatalf("confirmed calibration did not block quota at the limit: %+v", saved)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.calls != 2 || len(remote.commands) != 0 {
		t.Fatalf("unexpected quota calls = %d, remaining Gost commands = %v", remote.calls, remote.commands)
	}
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
			_, err := createServiceWithTrafficQuota(repo, utils.GetGostClient(&node), node, service)
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
		{"quota clear temporarily unavailable", codes.Unavailable, 1793548800, 100},
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
			NewRuleSyncService(db).syncNodeRules(node, nil)
			saved, err := repo.FindByID(node.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantUsed, wantWindow := "", uint64(0), int64(model.TrafficQuotaWindowUnset)
			if tc.quotaCode == codes.Unavailable {
				wantState, wantUsed, wantWindow = model.TrafficQuotaPending, tc.used, tc.window
			}
			if saved.TrafficQuotaState != wantState || saved.TrafficQuotaUsedBytes != wantUsed || saved.TrafficQuotaWindowStart != wantWindow {
				t.Errorf("quota after clearing = %q, %d bytes, window %d, want %q, %d, %d",
					saved.TrafficQuotaState, saved.TrafficQuotaUsedBytes, saved.TrafficQuotaWindowStart, wantState, wantUsed, wantWindow)
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
	mu         sync.Mutex
	check      func(*nodegrpc.TrafficQuotaRequest)
	used       uint64
	quotaErr   error
	calls      int
	calibrated bool
}

func (s *trafficQuotaTestNode) SyncTrafficQuota(_ context.Context, req *nodegrpc.TrafficQuotaRequest) (*nodegrpc.TrafficQuotaReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.check(req)
	if s.quotaErr != nil {
		return nil, s.quotaErr
	}
	return &nodegrpc.TrafficQuotaReply{UsedBytes: s.used, Calibrated: s.calibrated}, nil
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

func TestRuleSyncRecoversWhenConfigAndQuotaCommandsFail(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &trafficQuotaAdminTestNode{t: t}
	node := startServiceTestNode(t, remote)
	node.TrafficLimitBytes = 100
	node.TrafficQuotaState = model.TrafficQuotaPending
	rule := model.GostRule{Name: "edge", Type: model.RuleTypeForward, NodeID: &node.ID, ServiceID: "rule-9", Status: model.RuleStatusStopped}
	saveServiceFixtures(t, db, &node, &rule)
	remote.check = func(req *nodegrpc.TrafficQuotaRequest) {
		if req.GetLimitBytes() != 100 || !slices.Equal(req.GetTcpPorts(), []uint32{5010}) {
			t.Errorf("unexpected quota update: %v", req)
		}
	}
	remote.used = 33

	service := NewRuleSyncService(db)
	const config = `{"services":[{"name":"rule-9","addr":":5010","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"running"}}]}`
	for _, tc := range []struct {
		name            string
		admin           trafficQuotaAdminCommand
		quotaErr        error
		wantCalls       int
		wantQuota       string
		wantRule        model.RuleStatus
		tunnelReadFails bool
	}{
		{"Gost config temporarily unavailable", trafficQuotaAdminCommand{"GET /config", http.StatusServiceUnavailable, ""}, nil, 0, model.TrafficQuotaPending, model.RuleStatusStopped, false},
		{"quota command temporarily unavailable", trafficQuotaAdminCommand{"GET /config", http.StatusOK, config}, status.Error(codes.Unavailable, "node temporarily unavailable"), 1, model.TrafficQuotaPending, model.RuleStatusRunning, false},
		{"node commands recover", trafficQuotaAdminCommand{"GET /config", http.StatusOK, config}, nil, 2, model.TrafficQuotaActive, model.RuleStatusRunning, false},
		{"rule sync continues without tunnel storage", trafficQuotaAdminCommand{"GET /config", http.StatusOK, config}, nil, 3, model.TrafficQuotaActive, model.RuleStatusRunning, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tunnelReadFails {
				requireServiceError(t, db.Model(&rule).Update("status", model.RuleStatusStopped).Error, nil)
				requireServiceError(t, db.Migrator().DropTable(&model.GostTunnel{}), nil)
			}
			remote.mu.Lock()
			remote.commands = []trafficQuotaAdminCommand{tc.admin}
			remote.quotaErr = tc.quotaErr
			remote.mu.Unlock()
			service.syncNodeRules(node, nil)
			requireServiceError(t, db.First(&node, node.ID).Error, nil)
			requireServiceError(t, db.First(&rule, rule.ID).Error, nil)
			if node.TrafficQuotaState != tc.wantQuota || rule.Status != tc.wantRule {
				t.Fatalf("sync stored quota state %q and rule state %q, want %q and %q", node.TrafficQuotaState, rule.Status, tc.wantQuota, tc.wantRule)
			}
			if tc.wantQuota == model.TrafficQuotaActive && node.TrafficQuotaUsedBytes != 33 {
				t.Fatalf("sync saved %d quota bytes, want 33", node.TrafficQuotaUsedBytes)
			}
			remote.mu.Lock()
			defer remote.mu.Unlock()
			if len(remote.commands) != 0 || remote.calls != tc.wantCalls {
				t.Fatalf("remaining commands = %v, quota calls = %d, want %d", remote.commands, remote.calls, tc.wantCalls)
			}
		})
	}
}
