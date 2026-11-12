package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestAutomaticDaemonUpgradeContinuesAfterConfirmedFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *nodegrpc.NodeUpgradeStatus
		err      error
		busy     bool
		want     []string
	}{
		{
			name: "worker rejects launch", err: status.Error(codes.Unavailable, "worker temporarily unavailable"),
			want: []string{"daemon/0.1.90-abcdef0"},
		},
		{
			name: "worker returns confirmed failure", response: &nodegrpc.NodeUpgradeStatus{State: "failed", Error: "release download failed"},
			want: []string{"daemon/0.1.90-abcdef0"},
		},
		{name: "a worker is already running", busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newServiceTestDB(t)
			first := &automaticUpgradeRecoveryNode{
				nodeUpgradeFixture: nodeUpgradeFixture{daemon: "0.1.89-abcdef0", started: make(chan string, 4)},
				response:           tc.response, err: tc.err, busy: tc.busy,
			}
			second := &nodeUpgradeFixture{daemon: "0.1.89-abcdef0", started: make(chan string, 4)}
			node, other := startServiceTestNode(t, first), startServiceTestNode(t, second)
			other.Name = "second"
			node.UpgradePolicy.AutoUpgradeDaemon, other.UpgradePolicy.AutoUpgradeDaemon = true, true
			saveServiceFixtures(t, db, &node, &other)

			s := NewNodeUpgradeService(db)
			s.panelVersion = "0.1.90-abcdef0"
			s.runAutomatic(t.Context())
			close(first.started)
			var started []string
			for attempt := range first.started {
				started = append(started, attempt)
			}
			if !slices.Equal(started, tc.want) {
				t.Fatalf("upgrade attempts = %v, want %v", started, tc.want)
			}
			if len(second.started) != 1 || <-second.started != "daemon/0.1.90-abcdef0" {
				t.Fatal("automatic upgrade did not continue to the next node")
			}
		})
	}
}

func TestAutomaticUpgradeStopsWhenWorkerCannotConfirmCompletion(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &automaticUpgradeRecoveryNode{
		nodeUpgradeFixture: nodeUpgradeFixture{daemon: "0.1.89-abcdef0", started: make(chan string, 4)},
		response:           &nodegrpc.NodeUpgradeStatus{State: "queued", Component: "daemon", TargetVersion: "0.1.90-abcdef0"},
	}
	second := &nodeUpgradeFixture{daemon: "0.1.89-abcdef0", started: make(chan string, 4)}
	node, other := startServiceTestNode(t, remote), startServiceTestNode(t, second)
	other.Name = "second"
	node.UpgradePolicy.AutoUpgradeDaemon, other.UpgradePolicy.AutoUpgradeDaemon = true, true
	saveServiceFixtures(t, db, &node, &other)
	s := NewNodeUpgradeService(db)
	s.panelVersion = "0.1.90-abcdef0"

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	s.runAutomatic(ctx)
	requireServiceError(t, ctx.Err(), context.DeadlineExceeded)
	if len(remote.started) != 1 || <-remote.started != "daemon/0.1.90-abcdef0" || len(second.started) != 0 {
		t.Fatal("automatic upgrade must wait for the first daemon worker to confirm completion")
	}
}

type automaticUpgradeRecoveryNode struct {
	nodeUpgradeFixture
	response *nodegrpc.NodeUpgradeStatus
	err      error
	busy     bool
}

func (n *automaticUpgradeRecoveryNode) GetUpgradeStatus(ctx context.Context, request *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	if n.busy {
		return &nodegrpc.NodeUpgradeStatus{Supported: true, State: "running", Component: "daemon"}, nil
	}
	return n.nodeUpgradeFixture.GetUpgradeStatus(ctx, request)
}

func (n *automaticUpgradeRecoveryNode) StartUpgrade(_ context.Context, request *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	n.started <- request.GetComponent() + "/" + request.GetVersion()
	return n.response, n.err
}
