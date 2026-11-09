package service

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestAutomaticUpgradeRecoversAfterReleaseAndWorkerFailures(t *testing.T) {
	var failRelease atomic.Bool
	var gostLookups atomic.Int32
	useLocalReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/go-gost/gost/releases/latest":
			gostLookups.Add(1)
			if failRelease.Load() {
				http.Error(w, "release unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"tag_name":"v3.2.8"}`))
		case "/repos/hanzec/yagp/releases/latest":
			t.Error("daemon policy queried the remote latest release")
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	for _, tc := range []struct {
		name               string
		releaseUnavailable bool
		gostResponse       *nodegrpc.NodeUpgradeStatus
		gostError          error
		busy               bool
		want               []string
	}{
		{
			name: "release lookup fails only for Gost", releaseUnavailable: true,
			want: []string{"daemon/0.1.90-abcdef0"},
		},
		{
			name: "Gost worker rejects launch", gostError: status.Error(codes.Unavailable, "worker temporarily unavailable"),
			want: []string{"gost/v3.2.8", "daemon/0.1.90-abcdef0"},
		},
		{
			name:         "Gost worker returns confirmed failure",
			gostResponse: &nodegrpc.NodeUpgradeStatus{State: "failed", Error: "release download failed"},
			want:         []string{"gost/v3.2.8", "daemon/0.1.90-abcdef0"},
		},
		{name: "a worker is already running", busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failRelease.Store(tc.releaseUnavailable)
			db := newServiceTestDB(t)
			remote := &automaticUpgradeRecoveryNode{
				nodeUpgradeFixture: nodeUpgradeFixture{gost: "v3.2.7", daemon: "0.1.89-abcdef0", started: make(chan string, 4)},
				gostResponse:       tc.gostResponse, gostError: tc.gostError, busy: tc.busy,
			}
			node := startServiceTestNode(t, remote)
			node.UpgradePolicy = model.NodeUpgradePolicy{AutoUpgradeGost: true, AutoUpgradeDaemon: true}
			saveServiceFixtures(t, db, &node)

			service := NewNodeUpgradeService(db)
			service.panelVersion = "0.1.90-abcdef0"
			service.runAutomatic(t.Context())
			close(remote.started)
			var started []string
			for attempt := range remote.started {
				started = append(started, attempt)
			}
			if !slices.Equal(started, tc.want) {
				t.Fatalf("upgrade attempts = %v, want %v", started, tc.want)
			}
		})
	}
	failRelease.Store(false)
	service := NewNodeUpgradeService(newServiceTestDB(t))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := service.cachedLatest(canceled, "gost")
	requireServiceError(t, err, context.Canceled)
	before := gostLookups.Load()
	version, err := service.cachedLatest(t.Context(), "gost")
	requireServiceError(t, err, nil)
	if version != "v3.2.8" || gostLookups.Load() != before+1 {
		t.Fatalf("canceled lookup poisoned the next release lookup: version=%q, requests=%d", version, gostLookups.Load()-before)
	}
}

func TestAutomaticUpgradeStopsWhenWorkerCannotConfirmCompletion(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &automaticUpgradeRecoveryNode{
		nodeUpgradeFixture: nodeUpgradeFixture{gost: "v3.2.7", daemon: "0.1.89-abcdef0", started: make(chan string, 4)},
		gostResponse:       &nodegrpc.NodeUpgradeStatus{State: "queued", Component: "gost", TargetVersion: "v3.2.8"},
	}
	node := startServiceTestNode(t, remote)
	node.UpgradePolicy = model.NodeUpgradePolicy{AutoUpgradeGost: true, AutoUpgradeDaemon: true}
	saveServiceFixtures(t, db, &node)
	service := NewNodeUpgradeService(db)
	service.cache["gost"] = nodeUpgradeRelease{version: "v3.2.8", expiresAt: time.Now().Add(time.Minute)}
	service.panelVersion = "0.1.90-abcdef0"

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	service.runAutomatic(ctx)
	requireServiceError(t, ctx.Err(), context.DeadlineExceeded)
	if len(remote.started) != 1 || <-remote.started != "gost/v3.2.8" {
		t.Fatal("automatic upgrade started another component before the Gost worker confirmed completion")
	}
}

type automaticUpgradeRecoveryNode struct {
	nodeUpgradeFixture
	gostResponse *nodegrpc.NodeUpgradeStatus
	gostError    error
	busy         bool
}

func (n *automaticUpgradeRecoveryNode) GetUpgradeStatus(ctx context.Context, request *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	if n.busy {
		return &nodegrpc.NodeUpgradeStatus{Supported: true, State: "running", Component: "daemon"}, nil
	}
	return n.nodeUpgradeFixture.GetUpgradeStatus(ctx, request)
}

func (n *automaticUpgradeRecoveryNode) StartUpgrade(_ context.Context, request *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	n.started <- request.GetComponent() + "/" + request.GetVersion()
	if request.GetComponent() == "gost" {
		return n.gostResponse, n.gostError
	}
	return &nodegrpc.NodeUpgradeStatus{State: "succeeded"}, nil
}
