package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

func TestAutomaticNodeUpgradeIgnoresLegacyGostPolicy(t *testing.T) {
	t.Setenv("GH_PROXY", "")
	useLocalReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("automatic upgrades queried release metadata at %s", r.URL)
		http.NotFound(w, r)
	})
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
	if err := db.AutoMigrate(&model.GostNode{}, &model.OperationLog{}); err != nil {
		t.Fatal(err)
	}
	var nodes [2]model.GostNode
	var fixtures [2]*nodeUpgradeFixture
	for i := range fixtures {
		fixtures[i] = &nodeUpgradeFixture{gost: "3.2.5", daemon: "0.1.72-04dcad1", started: make(chan string, 8)}
		nodes[i] = startServiceTestNode(t, fixtures[i])
	}
	nodes[0].UpgradePolicy.AutoUpgradeGost = true
	nodes[1].Name = "second"
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := NewNodeUpgradeService(db)
	service.panelVersion = "0.1.73-abcdef0"
	service.runAutomatic(ctx)
	if len(fixtures[0].started)+len(fixtures[1].started) != 0 {
		t.Fatal("a legacy Gost policy must not trigger automatic upgrades")
	}
	logs, total, err := NewLogService(db).List(&dto.LogListReq{Action: model.ActionAutoUpgrade})
	requireServiceError(t, err, nil)
	if total != 0 {
		t.Fatalf("disabled policies emitted audit events: %+v", logs)
	}
	if err := service.SetPolicy(ctx, nodes[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPolicy(ctx, nodes[1].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&nodes[1]).Update("auto_upgrade_gost", true).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := service.findNode(ctx, nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UpgradePolicy.AutoUpgradeGost || stored.UpgradePolicy.AutoUpgradeDaemon ||
		stored.Username != nodes[0].Username || stored.Password != nodes[0].Password {
		t.Fatal("saving the daemon policy must clear the old Gost setting and preserve node credentials")
	}
	service.runAutomatic(ctx)
	service.runAutomatic(ctx)
	if len(fixtures[0].started) != 0 || len(fixtures[1].started) != 1 || <-fixtures[1].started != "daemon/0.1.73-abcdef0" {
		t.Fatal("automatic upgrade must only start yagp-daemon for the opted-in node")
	}

	logs, total, err = NewLogService(db).List(&dto.LogListReq{Action: model.ActionAutoUpgrade, ResourceType: model.ResourceTypeNode})
	requireServiceError(t, err, nil)
	if total != 2 {
		t.Fatalf("expected one start and one result, without duplicate events on the next pass; got %+v", logs)
	}
	for _, entry := range logs {
		if entry.ResourceID != nodes[1].ID || entry.Username != "系统" || entry.IPAddress != "" || entry.UserAgent != "" ||
			!strings.Contains(entry.Details, nodes[1].Name) || !strings.Contains(entry.Details, "yagp-daemon") {
			t.Fatalf("automatic event missing attribution or node/component: %+v", entry)
		}
	}
	if !strings.Contains(logs[0].Details, "自动升级成功") || !strings.Contains(logs[1].Details, "自动升级已启动") {
		t.Fatalf("automatic event sequence = %+v", logs)
	}
}

func TestAutomaticNodeUpgradeRecordsFailuresAndPendingConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start      *nodegrpc.NodeUpgradeStatus
		startError error
		want       string
		wantCount  int64
		wait       bool
	}{
		{"start rejected", nil, status.Error(codes.Unavailable, "node offline"), "自动升级启动失败", 1, false},
		{"worker failed", &nodegrpc.NodeUpgradeStatus{State: "failed", Error: "checksum mismatch"}, nil, "自动升级失败：checksum mismatch", 2, false},
		{"confirmation unavailable", &nodegrpc.NodeUpgradeStatus{State: "queued", Component: "daemon", TargetVersion: "0.1.73-abcdef0"}, nil, "自动升级结果待确认", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newServiceTestDB(t)
			remote := &automaticUpgradeAuditNode{
				nodeUpgradeFixture: nodeUpgradeFixture{daemon: "0.1.72-abcdef0"},
				start:              tc.start, startError: tc.startError,
			}
			node := startServiceTestNode(t, remote)
			node.UpgradePolicy.AutoUpgradeDaemon = true
			saveServiceFixtures(t, db, &node)
			s := NewNodeUpgradeService(db)
			ctx := context.Background()
			if tc.wait {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			err := s.upgradeAutomatically(ctx, node.ID, "0.1.73-abcdef0")
			if tc.wait {
				requireServiceError(t, err, context.DeadlineExceeded)
			} else {
				requireServiceError(t, err, nil)
			}
			logs, total, err := NewLogService(db).List(&dto.LogListReq{Action: model.ActionAutoUpgrade})
			requireServiceError(t, err, nil)
			if total != tc.wantCount || !strings.Contains(logs[0].Details, tc.want) ||
				!strings.Contains(logs[0].Details, "0.1.72-abcdef0 → 0.1.73-abcdef0") || logs[0].ResourceID != node.ID {
				t.Fatalf("automatic audit events = %+v", logs)
			}
			for _, entry := range logs {
				if strings.Contains(entry.Details, "自动升级成功") {
					t.Fatalf("unconfirmed or failed upgrade reported success: %+v", entry)
				}
			}
		})
	}
}

func TestAutomaticNodeUpgradeFinishesRuntimeMigrationInSamePass(t *testing.T) {
	const target = "0.1.73-abcdef0"
	for _, test := range []struct {
		name      string
		collision bool
		failed    bool
	}{
		{name: "old worker still exiting", collision: true},
		{name: "migration failed", failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newServiceTestDB(t)
			remote := &runtimeMigrationUpgradeNode{
				collision: test.collision, failed: test.failed,
				state: &nodegrpc.NodeUpgradeStatus{Supported: true, DaemonVersion: "0.1.72-abcdef0"},
			}
			node := startServiceTestNode(t, remote)
			node.UpgradePolicy.AutoUpgradeDaemon = true
			saveServiceFixtures(t, db, &node)
			remote.mu.Lock()
			remote.nodeID = node.ID
			remote.mu.Unlock()
			s := NewNodeUpgradeService(db)
			s.panelVersion = target
			ctx, cancel := context.WithTimeout(t.Context(), 4*nodeUpgradePoll+3*time.Second)
			defer cancel()
			s.runAutomatic(ctx)
			if err := ctx.Err(); err != nil {
				t.Fatal(err)
			}
			remote.mu.Lock()
			starts := append([]string(nil), remote.starts...)
			remote.mu.Unlock()
			wantStarts := 2
			if test.collision {
				wantStarts++
			}
			if len(starts) != wantStarts {
				t.Fatalf("started upgrades = %v, want %d attempts", starts, wantStarts)
			}
			for _, start := range starts {
				if start != "daemon/"+target {
					t.Fatalf("unexpected upgrade %s", start)
				}
			}
			logs, total, err := NewLogService(db).List(&dto.LogListReq{Action: model.ActionAutoUpgrade})
			requireServiceError(t, err, nil)
			wantResult := "自动升级成功"
			if test.failed {
				wantResult = "自动升级失败：file conflict"
			}
			if total != 4 || !strings.Contains(logs[0].Details, wantResult) {
				t.Fatalf("migration audit events = %+v", logs)
			}
		})
	}
}

type runtimeMigrationUpgradeNode struct {
	nodegrpc.UnimplementedV1Server
	mu        sync.Mutex
	state     *nodegrpc.NodeUpgradeStatus
	starts    []string
	finish    bool
	failed    bool
	collision bool
	nodeID    uint
}

func (n *runtimeMigrationUpgradeNode) GetUpgradeStatus(context.Context, *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	current := n.state
	if n.finish {
		n.finish = false
		result := &nodegrpc.NodeUpgradeStatus{Supported: true, Component: current.GetComponent(), TargetVersion: current.GetTargetVersion(),
			DaemonVersion: current.GetDaemonVersion(), State: "succeeded"}
		if n.failed {
			result.State, result.Error, result.RuntimeUpdateRequired = "failed", "file conflict", true
		}
		n.state = result
		// On success, the first poll can still see the previous worker's result.
		if n.failed {
			return result, nil
		}
	}
	return current, nil
}

func (n *runtimeMigrationUpgradeNode) StartUpgrade(_ context.Context, req *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.starts = append(n.starts, req.GetComponent()+"/"+req.GetVersion())
	if len(n.starts) == 2 && n.collision {
		return nil, status.Errorf(codes.FailedPrecondition, "Unit yagp-upgrade-%d.service was already loaded", n.nodeID)
	}
	if len(n.starts) == 1 {
		n.state = &nodegrpc.NodeUpgradeStatus{Supported: true, Component: req.GetComponent(), TargetVersion: req.GetVersion(),
			DaemonVersion: req.GetVersion(), State: "succeeded", RuntimeUpdateRequired: true}
	} else {
		n.finish = true
	}
	return &nodegrpc.NodeUpgradeStatus{Supported: true, Component: req.GetComponent(), TargetVersion: req.GetVersion(), State: "queued"}, nil
}

type automaticUpgradeAuditNode struct {
	nodeUpgradeFixture
	start      *nodegrpc.NodeUpgradeStatus
	startError error
}

func (n *automaticUpgradeAuditNode) StartUpgrade(context.Context, *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	return n.start, n.startError
}

type nodeUpgradeFixture struct {
	nodegrpc.UnimplementedV1Server
	mu           sync.Mutex
	gost, daemon string
	started      chan string
}

func (n *nodeUpgradeFixture) GetUpgradeStatus(context.Context, *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &nodegrpc.NodeUpgradeStatus{Supported: true, State: "idle", GostVersion: n.gost, DaemonVersion: n.daemon}, nil
}

func (n *nodeUpgradeFixture) StartUpgrade(_ context.Context, req *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if req.GetComponent() == "gost" {
		n.gost = req.GetVersion()
	} else {
		n.daemon = req.GetVersion()
	}
	n.started <- req.GetComponent() + "/" + req.GetVersion()
	return &nodegrpc.NodeUpgradeStatus{Supported: true, State: "succeeded", GostVersion: n.gost, DaemonVersion: n.daemon}, nil
}
