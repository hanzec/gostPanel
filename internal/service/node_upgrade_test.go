package service

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

func TestAutomaticNodeUpgradeUsesStoredComponentPolicy(t *testing.T) {
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
	var nodes [2]model.GostNode
	var fixtures [2]*nodeUpgradeFixture
	for i := range fixtures {
		fixtures[i] = &nodeUpgradeFixture{gost: "3.2.5", daemon: "0.1.72-04dcad1", started: make(chan string, 8)}
		nodes[i] = startServiceTestNode(t, fixtures[i])
	}
	nodes[1].Name = "second"
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := NewNodeUpgradeService(db)
	validUntil := time.Now().Add(time.Minute)
	service.cache["gost"] = nodeUpgradeRelease{version: "v3.2.6", expiresAt: validUntil}
	service.panelVersion = "0.1.73-abcdef0"
	service.runAutomatic(ctx)
	if len(fixtures[0].started)+len(fixtures[1].started) != 0 {
		t.Fatal("new nodes must not be upgraded until an administrator enables their policy")
	}
	if err := service.SetPolicy(ctx, nodes[0].ID, model.NodeUpgradePolicy{AutoUpgradeGost: true, AutoUpgradeDaemon: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPolicy(ctx, nodes[0].ID, model.NodeUpgradePolicy{AutoUpgradeGost: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPolicy(ctx, nodes[1].ID, model.NodeUpgradePolicy{AutoUpgradeDaemon: true}); err != nil {
		t.Fatal(err)
	}
	stored, err := service.findNode(ctx, nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.UpgradePolicy.AutoUpgradeGost || stored.UpgradePolicy.AutoUpgradeDaemon ||
		stored.Username != nodes[0].Username || stored.Password != nodes[0].Password {
		t.Fatal("updating one automatic upgrade setting changed the other setting or the node credentials")
	}
	service.runAutomatic(ctx)
	service.runAutomatic(ctx)
	for i, want := range []string{"gost/v3.2.6", "daemon/0.1.73-abcdef0"} {
		close(fixtures[i].started)
		var started []string
		for operation := range fixtures[i].started {
			started = append(started, operation)
		}
		if !slices.Equal(started, []string{want}) {
			t.Errorf("node %d upgrade requests = %v; want %q", nodes[i].ID, started, want)
		}
	}
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
