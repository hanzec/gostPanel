package service

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

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

	fixture := &trafficQuotaTestNode{}
	node := startServiceTestNode(t, fixture)
	node.TrafficLimitBytes, node.TotalBytes = 100, 40
	node.TrafficQuotaState = model.TrafficQuotaPending
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	repo := repository.NewNodeRepository(db)
	client := utils.GetGostClient(&node)
	services := []gost.ServiceConfig{
		{Name: "rule-1", Addr: ":5010", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
		{Name: "rule-2", Addr: ":5011", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "udp"}},
		{Name: "relay-tunnel-3", Addr: "[::]:5012", Handler: &gost.HandlerConfig{Type: "relay"}, Listener: &gost.ListenerConfig{Type: "quic"}},
		{Name: "other-service", Addr: ":5013", Handler: &gost.HandlerConfig{Type: "forward"}, Listener: &gost.ListenerConfig{Type: "tcp"}},
	}
	now := time.Date(2026, 11, 8, 15, 59, 59, 0, time.UTC)
	sync := func(limit, seed, used uint64, window int64, state string) {
		t.Helper()
		fixture.mu.Lock()
		fixture.check = func(req *nodegrpc.TrafficQuotaRequest) {
			if req.GetLimitBytes() != limit || req.GetUsedBytes() != seed || req.GetWindowStart() != window {
				t.Errorf("quota request = %v, want limit %d, seed %d, window %d", req, limit, seed, window)
			}
			if limit > 0 && (!slices.Equal(req.GetTcpPorts(), []uint32{5010}) || !slices.Equal(req.GetUdpPorts(), []uint32{5011, 5012})) {
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
	zero := int64(0)
	if err := repo.Update(&node, &zero, nil); err != nil {
		t.Fatal(err)
	}
	sync(0, 0, 0, model.TrafficQuotaWindowUnset, "")
}

type trafficQuotaTestNode struct {
	nodegrpc.UnimplementedNodeServer
	mu    sync.Mutex
	check func(*nodegrpc.TrafficQuotaRequest)
	used  uint64
}

func (s *trafficQuotaTestNode) SyncTrafficQuota(ctx context.Context, req *nodegrpc.TrafficQuotaRequest) (*nodegrpc.TrafficQuotaReply, error) {
	if !nodegrpc.MatchCredentials(ctx, "entry", "fixture-password") {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.check(req)
	return &nodegrpc.TrafficQuotaReply{UsedBytes: s.used}, nil
}
