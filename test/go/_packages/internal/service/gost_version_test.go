package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestNodeHealthStoresInstalledGostVersion(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &gostVersionNode{lifecycleNode: lifecycleNode{t: t}, version: "v3.2.7"}
	node := startServiceTestNode(t, remote)
	saveServiceFixtures(t, db, &node)
	health := NewNodeHealthService(db)
	nodes := NewNodeService(db, "")

	health.checkAll()
	var saved *model.GostNode
	waitServiceCondition(t, func() bool {
		var err error
		saved, err = nodes.GetByID(node.ID)
		requireServiceError(t, err, nil)
		return saved.LastCheckAt != nil && saved.GostVersion == "v3.2.7"
	})

	listed, total, err := nodes.List(&dto.NodeListReq{})
	requireServiceError(t, err, nil)
	if total != 1 || len(listed) != 1 || listed[0].GostVersion != "v3.2.7" {
		t.Fatalf("node list = %+v (total %d), want installed Gost version v3.2.7", listed, total)
	}
	payload, err := json.Marshal(listed[0])
	requireServiceError(t, err, nil)
	var fields map[string]any
	requireServiceError(t, json.Unmarshal(payload, &fields), nil)
	if fields["gost_version"] != "v3.2.7" {
		t.Fatalf("serialized installed Gost version = %v, want v3.2.7", fields["gost_version"])
	}
	if _, present := fields["gost_version_checked_at"]; present {
		t.Fatal("node response exposed the installed version check timestamp")
	}

	now := time.Now()
	remote.mu.Lock()
	remote.version = ""
	remote.mu.Unlock()
	health.refreshGostVersion(*saved, now)
	if calls := remote.calls.Load(); calls != 1 {
		t.Fatalf("installed version checks = %d, want one within the refresh interval", calls)
	}

	health.refreshGostVersion(*saved, now.Add(6*time.Minute))
	saved, err = nodes.GetByID(node.ID)
	requireServiceError(t, err, nil)
	if calls := remote.calls.Load(); calls != 2 || saved.GostVersion != "v3.2.7" {
		t.Fatalf("after failed check: checks = %d, installed version = %q", calls, saved.GostVersion)
	}

	remote.mu.Lock()
	remote.version = "v3.2.8"
	remote.mu.Unlock()
	health.refreshGostVersion(*saved, now.Add(12*time.Minute))
	saved, err = nodes.GetByID(node.ID)
	requireServiceError(t, err, nil)
	if saved.GostVersion != "v3.2.8" {
		t.Fatalf("installed version after refresh = %q, want v3.2.8", saved.GostVersion)
	}
}

type gostVersionNode struct {
	lifecycleNode
	version string
	calls   atomic.Int32
}

func (n *gostVersionNode) GetUpgradeStatus(context.Context, *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	n.calls.Add(1)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.version == "" {
		return nil, status.Error(codes.Unavailable, "installed version unavailable")
	}
	return &nodegrpc.NodeUpgradeStatus{Supported: true, GostVersion: n.version}, nil
}
