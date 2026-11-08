package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestManualUpgradeValidatesStateAndStartsStableRelease(t *testing.T) {
	db := newServiceTestDB(t)
	remote := &upgradeWorkflowNode{}
	remote.started = make(chan string, 4)
	node := startServiceTestNode(t, remote)
	node.UpgradePolicy.AutoUpgradeGost = true
	saveServiceFixtures(t, db, &node)
	var releaseFailures atomic.Int32
	var requests atomic.Int32
	useLocalReleaseAPI(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/repos/go-gost/gost/releases/latest":
			if releaseFailures.Load()&1 != 0 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(w, "{\"tag_name\":\"v3.2.8\"}")
		case "/repos/hanzec/yagp/releases/latest":
			if releaseFailures.Load()&2 != 0 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(w, "{\"tag_name\":\"0.1.90-abcdef0\"}")
		default:
			http.NotFound(w, r)
		}
	})
	s := NewNodeUpgradeService(db)
	ctx := context.Background()

	_, err := s.Get(ctx, 99)
	requireServiceError(t, err, bizerrors.ErrNodeNotFound)
	requireServiceError(t, s.SetPolicy(ctx, 99, model.NodeUpgradePolicy{}), bizerrors.ErrNodeNotFound)
	_, err = s.Upgrade(ctx, 99, "daemon")
	requireServiceError(t, err, bizerrors.ErrNodeNotFound)
	_, err = s.Upgrade(ctx, node.ID, "unknown")
	requireUpgradeCode(t, err, codes.InvalidArgument)

	for _, tc := range []struct {
		name   string
		state  *nodegrpc.NodeUpgradeStatus
		err    error
		code   codes.Code
		reason string
	}{
		{"missing RPC", nil, status.Error(codes.Unimplemented, "not supported"), codes.Unimplemented, "当前节点守护程序版本不支持在线升级"},
		{"daemon precondition", nil, status.Error(codes.FailedPrecondition, "test daemon unavailable"), codes.FailedPrecondition, "test daemon unavailable"},
		{"lost connection", nil, status.Error(codes.Unavailable, "offline"), codes.Unavailable, "无法读取节点升级状态，请检查节点连接"},
		{"not installed as a service", &nodegrpc.NodeUpgradeStatus{}, nil, codes.Unimplemented, "在线升级需要通过安装脚本部署的 Linux systemd 节点"},
		{"active task", &nodegrpc.NodeUpgradeStatus{Supported: true, State: "running", Component: "gost", TargetVersion: "v3.2.8", GostVersion: "v3.2.7"}, nil, codes.FailedPrecondition, ""},
		{"unrecognized installed version", &nodegrpc.NodeUpgradeStatus{Supported: true}, nil, codes.FailedPrecondition, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote.setStatus(tc.state, tc.err)
			if tc.reason != "" {
				view, err := s.Get(ctx, node.ID)
				requireServiceError(t, err, nil)
				if view.Available || view.Reason != tc.reason || !view.AutoUpgradeGost {
					t.Fatalf("upgrade status = %+v", view)
				}
			}
			_, err := s.Upgrade(ctx, node.ID, "gost")
			requireUpgradeCode(t, err, tc.code)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid upgrades contacted release API %d times", requests.Load())
	}
	remote.setStatus(&nodegrpc.NodeUpgradeStatus{Supported: true, State: "idle", GostVersion: "v3.2.7", DaemonVersion: "0.1.89-abcdef1"}, nil)
	view, err := s.Get(ctx, node.ID)
	requireServiceError(t, err, nil)
	if !view.Available || !view.GostUpdateAvailable || !view.DaemonUpdateAvailable || view.Reason != "" ||
		view.LatestGostVersion != "v3.2.8" || view.LatestDaemonVersion != "0.1.90-abcdef0" {
		t.Fatalf("available upgrades = %+v", view)
	}
	_, err = s.Get(ctx, node.ID)
	requireServiceError(t, err, nil)
	if requests.Load() != 2 {
		t.Fatalf("version lookup failed to reuse the two cached responses: %d requests", requests.Load())
	}
	response, err := s.Upgrade(ctx, node.ID, "gost")
	requireServiceError(t, err, nil)
	if response.GetState() != "succeeded" || len(remote.started) != 1 || <-remote.started != "gost/v3.2.8" {
		t.Fatalf("upgrade response = %+v", response)
	}
	remote.setStatus(&nodegrpc.NodeUpgradeStatus{Supported: true, GostVersion: "v3.2.8"}, nil)
	_, err = s.Upgrade(ctx, node.ID, "gost")
	requireUpgradeCode(t, err, codes.FailedPrecondition)

	releaseFailures.Store(1)
	_, err = s.Upgrade(ctx, node.ID, "gost")
	if e, ok := err.(*bizerrors.BizError); !ok || e.HTTPCode != http.StatusBadGateway {
		t.Fatalf("upstream lookup error = %v", err)
	}
	for _, tc := range []struct {
		failures int32
		reason   string
	}{
		{1, "暂时无法查询 Gost 的最新稳定版本"},
		{2, "暂时无法查询 yagp-daemon 的最新稳定版本"},
		{3, "暂时无法查询 Gost 和 yagp-daemon 的最新稳定版本"},
	} {
		releaseFailures.Store(tc.failures)
		view, err := NewNodeUpgradeService(db).Get(ctx, node.ID)
		requireServiceError(t, err, nil)
		if view.Reason != tc.reason {
			t.Fatalf("partial release failures %d yielded %q", tc.failures, view.Reason)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	NewNodeUpgradeService(db).Run(canceled)
	_, err = waitForNodeUpgrade(canceled, utils.GetGostClient(&node), "gost", "v3.2.8")
	requireServiceError(t, err, context.Canceled)
}

func TestWaitForNodeUpgradeReturnsConfirmedResult(t *testing.T) {
	remote := &upgradeWorkflowNode{}
	remote.setStatus(&nodegrpc.NodeUpgradeStatus{Supported: true, Component: "gost", State: "succeeded", TargetVersion: "v3.2.8"}, nil)
	node := startServiceTestNode(t, remote)
	ctx, cancel := context.WithTimeout(context.Background(), nodeUpgradePoll+3*time.Second)
	defer cancel()
	state, err := waitForNodeUpgrade(ctx, utils.GetGostClient(&node), "gost", "v3.2.8")
	requireServiceError(t, err, nil)
	if state.GetState() != "succeeded" || state.GetTargetVersion() != "v3.2.8" {
		t.Fatalf("confirmation = %v", state)
	}
}

func requireUpgradeCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("upgrade error = %v, want code %s", err, code)
	}
}

type upgradeWorkflowNode struct {
	nodeUpgradeFixture
	state     *nodegrpc.NodeUpgradeStatus
	statusErr error
}

func (n *upgradeWorkflowNode) setStatus(state *nodegrpc.NodeUpgradeStatus, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state, n.statusErr = state, err
}

func (n *upgradeWorkflowNode) GetUpgradeStatus(context.Context, *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state, n.statusErr
}

// Redirect the GitHub HTTP boundary to a local server. The upgrade client still
// performs real HTTP requests and validates the returned release metadata.
func useLocalReleaseAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	requireServiceError(t, err, nil)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = localReleaseTransport{endpoint: endpoint, transport: server.Client().Transport}
}

type localReleaseTransport struct {
	endpoint  *url.URL
	transport http.RoundTripper
}

func (tr localReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.github.com" {
		return nil, fmt.Errorf("unexpected HTTP request to %s", req.URL.Host)
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = tr.endpoint.Scheme, tr.endpoint.Host
	clone.Host = ""
	return tr.transport.RoundTrip(clone)
}
