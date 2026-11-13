package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yagp/internal/buildinfo"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/service"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const handlerNodePassword = "node-test-password"

func (f *handlerHTTPFixture) enrollFixtureNode(t *testing.T, name string, nodeControl *handlerNodeControl) model.GostNode {
	t.Helper()
	node := model.GostNode{
		Name: name, Address: "127.0.0.1",
		Status: model.NodeStatusOnline, Username: name, Password: handlerNodePassword,
	}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	dispatch := nodegrpc.NewDispatcher(nodeControl,
		nodegrpc.V1_Execute_FullMethodName, nodegrpc.V1_GetGostServiceStatus_FullMethodName, nodegrpc.V1_RestartGostService_FullMethodName,
		nodegrpc.V1_GetGostLogs_FullMethodName, nodegrpc.V1_GetNftConfig_FullMethodName,
		nodegrpc.V1_GetUpgradeStatus_FullMethodName, nodegrpc.V1_StartUpgrade_FullMethodName, nodegrpc.V1_SyncTrafficQuota_FullMethodName)
	startTestReportStream(t, t.Context(), startTestReportServer(t, f.db), node, func(ctx context.Context, method string, body []byte) ([]byte, error) {
		if err := nodeControl.before(method); err != nil {
			return nil, err
		}
		return dispatch(ctx, method, body)
	})
	return node
}

type handlerNodeControl struct {
	nodegrpc.UnimplementedV1Server
	admin       http.Handler
	mu          sync.Mutex
	nft         string
	nftLive     string
	upgrade     *nodegrpc.NodeUpgradeRequest
	quota       *nodegrpc.TrafficQuotaRequest
	calibrates  bool
	failures    map[string]codes.Code
	restartCall int
}

func (n *handlerNodeControl) reject(method string, code codes.Code) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failures[method] = code
}

func (n *handlerNodeControl) before(method string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if code := n.failures[method]; code != codes.OK {
		return status.Error(code, "fixture node error")
	}
	return nil
}

func (n *handlerNodeControl) Execute(ctx context.Context, req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if n.admin == nil {
		return nil, status.Error(codes.Unimplemented, "fixture has no Gost HTTP API")
	}
	rec := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(ctx, req.Method, req.Path, bytes.NewReader(req.Body))
	n.admin.ServeHTTP(rec, request)
	return &nodegrpc.CommandReply{StatusCode: int32(rec.Code), Body: rec.Body.Bytes()}, nil
}

func (n *handlerNodeControl) GetGostServiceStatus(context.Context, *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	return &nodegrpc.GostServiceStatus{Unit: "gost.service", ActiveState: "active"}, nil
}

func (n *handlerNodeControl) RestartGostService(ctx context.Context, empty *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	n.mu.Lock()
	n.restartCall++
	n.mu.Unlock()
	return n.GetGostServiceStatus(ctx, empty)
}

func (n *handlerNodeControl) GetGostLogs(_ context.Context, req *nodegrpc.GostLogsRequest) (*nodegrpc.GostLogsReply, error) {
	if req.GetLines() != 100 {
		return nil, status.Error(codes.InvalidArgument, "unexpected log line limit")
	}
	return &nodegrpc.GostLogsReply{Entries: []*nodegrpc.GostLogEntry{{Message: "gost started"}}}, nil
}

func (n *handlerNodeControl) GetNftConfig(context.Context, *emptypb.Empty) (*nodegrpc.NftConfig, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &nodegrpc.NftConfig{Config: n.nft, LiveConfig: n.nftLive}, nil
}

func (n *handlerNodeControl) GetUpgradeStatus(context.Context, *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	return &nodegrpc.NodeUpgradeStatus{Supported: true, GostVersion: "3.0.0", DaemonVersion: "0.1.1-abcdef0"}, nil
}

func (n *handlerNodeControl) StartUpgrade(_ context.Context, req *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.upgrade = req
	return &nodegrpc.NodeUpgradeStatus{Component: req.GetComponent(), TargetVersion: req.GetVersion(), State: "queued"}, nil
}

func (n *handlerNodeControl) SyncTrafficQuota(_ context.Context, req *nodegrpc.TrafficQuotaRequest) (*nodegrpc.TrafficQuotaReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.quota = req
	if n.calibrates {
		return &nodegrpc.TrafficQuotaReply{UsedBytes: req.GetCalibratedUsedBytes(), Calibrated: true}, nil
	}
	return &nodegrpc.TrafficQuotaReply{UsedBytes: req.GetUsedBytes()}, nil
}

func TestNodeHTTPTrafficCalibration(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	control := &handlerNodeControl{
		failures: make(map[string]codes.Code),
		admin: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"services":[{"name":"rule-1","addr":":9000","handler":{"type":"forward"},"listener":{"type":"tcp"}}]}`)
		}),
	}
	node := f.enrollFixtureNode(t, "calibrate", control)
	path := fmt.Sprintf("/nodes/%d/traffic", node.ID)
	for _, invalid := range []string{`{`, `{"input_bytes":-1,"output_bytes":0}`, `{"input_bytes":1}`, `{"input_bytes":9223372036854775807,"output_bytes":1}`, `{"input_bytes":0,"quota_used_bytes":0}`, `{"quota_used_bytes":9223372036854775808}`} {
		f.request(t, "PUT", path, invalid, 400)
	}
	f.request(t, "PUT", "/nodes/0/traffic", `{"input_bytes":0,"output_bytes":0}`, 400)
	f.request(t, "PUT", "/nodes/999/traffic", `{"input_bytes":0,"output_bytes":0}`, 404)
	f.request(t, "PUT", path, `{"quota_used_bytes":0}`, 409)
	if err := f.db.Model(&node).Updates(map[string]any{
		"traffic_limit_bytes": 100, "traffic_quota_used_bytes": 140, "traffic_quota_state": model.TrafficQuotaBlocked, "traffic_quota_window_start": 0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "PUT", path, `{"input_bytes":30,"output_bytes":65}`, 200)
	readNode := func() model.GostNode {
		t.Helper()
		var stored model.GostNode
		if err := f.db.First(&stored, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}
	if stored := readNode(); stored.InputBytes != 30 || stored.OutputBytes != 65 || stored.TotalBytes != 95 || stored.TrafficQuotaUsedBytes != 140 {
		t.Fatalf("incorrect totals after calibration: %+v", stored)
	}
	f.request(t, "PUT", path, `{"quota_used_bytes":0}`, 501)
	if stored := readNode(); stored.TrafficQuotaUsedBytes != 140 {
		t.Fatalf("an old daemon changed the panel's quota counter: %d", stored.TrafficQuotaUsedBytes)
	}
	control.mu.Lock()
	control.calibrates = true
	control.mu.Unlock()
	f.request(t, "PUT", path, `{"quota_used_bytes":0}`, 200)
	if stored := readNode(); stored.TotalBytes != 95 || stored.TrafficQuotaUsedBytes != 0 || stored.TrafficQuotaState != model.TrafficQuotaActive {
		t.Fatalf("incorrect quota after calibration: %+v", stored)
	}
	control.mu.Lock()
	request := control.quota
	control.mu.Unlock()
	if request.CalibratedUsedBytes == nil || request.GetCalibratedUsedBytes() != 0 || request.GetUsedBytes() != 140 || len(request.TcpPorts) != 1 || request.TcpPorts[0] != 9000 {
		t.Fatalf("incorrect calibration RPC: %v", request)
	}
	f.request(t, "PUT", path, `{"quota_used_bytes":200}`, 200)
	if stored := readNode(); stored.TrafficQuotaUsedBytes != 200 || stored.TrafficQuotaState != model.TrafficQuotaBlocked {
		t.Fatalf("raising usage above the limit failed: %+v", stored)
	}
	refreshed := handlerData[nodeTrafficResponse](t, f.request(t, "GET", fmt.Sprintf("/nodes/%d", node.ID), "", 200))
	if refreshed.CurrentPeriodUsedBytes == nil || *refreshed.CurrentPeriodUsedBytes != 200 {
		t.Fatalf("node detail did not report the calibrated quota: %+v", refreshed)
	}
}

func TestNodeHTTPCurrentPeriodTraffic(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	now := time.Now()
	currentWindow := model.TrafficQuotaWindow("monthly", now)
	for _, tc := range []struct {
		name        string
		limit       int64
		state       string
		window      int64
		used        uint64
		wantCurrent bool
	}{
		{"current", 100, model.TrafficQuotaBlocked, currentWindow, 120, true},
		{"empty", 100, model.TrafficQuotaActive, currentWindow, 0, true},
		{"previous", 100, model.TrafficQuotaActive, currentWindow - 1, 42, false},
		{"pending", 100, model.TrafficQuotaPending, currentWindow, 42, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := model.GostNode{
				Name: tc.name, TrafficLimitBytes: tc.limit, TrafficResetPeriod: "monthly",
				TrafficQuotaState: tc.state, TrafficQuotaWindowStart: tc.window, TrafficQuotaUsedBytes: tc.used,
				TotalBytes: 250,
			}
			if err := f.db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			check := func(got nodeTrafficResponse) {
				t.Helper()
				if (got.CurrentPeriodUsedBytes != nil) != tc.wantCurrent ||
					got.CurrentPeriodUsedBytes != nil && *got.CurrentPeriodUsedBytes != tc.used {
					t.Fatalf("unexpected current-period usage: %v", got.CurrentPeriodUsedBytes)
				}
				if got.ID != node.ID || got.TotalBytes != node.TotalBytes || got.TrafficQuotaUsedBytes != tc.used {
					t.Fatalf("node response lost existing traffic fields: %+v", got)
				}
			}
			path := fmt.Sprintf("/nodes/%d", node.ID)
			check(handlerData[nodeTrafficResponse](t, f.request(t, "GET", path, "", 200)))
			page := handlerData[handlerPage[nodeTrafficResponse]](t, f.request(t, "GET", "/nodes?keyword="+tc.name, "", 200))
			if len(page.List) != 1 {
				t.Fatalf("expected one node, got %+v", page)
			}
			check(page.List[0])
		})
	}
}

func TestNodeHTTPUncappedTrafficIncludesExistingRelaySamples(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	nodes := []model.GostNode{
		{Name: "shared exit", TrafficResetPeriod: "monthly", TrafficQuotaState: model.TrafficQuotaPending, TrafficQuotaUsedBytes: 999},
		{Name: "entry with observer traffic"},
		{Name: "zero-byte exit"},
		{Name: "unsampled node"},
		{Name: "capped exit", TrafficLimitBytes: 500, TrafficQuotaState: model.TrafficQuotaActive, TrafficQuotaUsedBytes: 66},
	}
	mustSucceed := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustSucceed(f.db.Create(&nodes).Error)
	shared, entry, zero, unsampled, capped := nodes[0].ID, nodes[1].ID, nodes[2].ID, nodes[3].ID, nodes[4].ID
	mustSucceed(f.db.Model(&nodes[4]).Update("traffic_quota_window_start", 0).Error)
	// A shared exit may have counted traffic per tunnel before it started counting per endpoint.
	exits := []model.GostRule{
		{NodeID: &shared, Name: "shared", Role: model.EndpointRoleExit, ListenPort: 8001},
		{NodeID: &zero, Name: "observed zero", Role: model.EndpointRoleExit, ListenPort: 8002},
		{NodeID: &capped, Name: "quota", Role: model.EndpointRoleExit, ListenPort: 8003},
		{NodeID: &shared, Name: "archived", Role: model.EndpointRoleExit, ListenPort: 8004},
	}
	mustSucceed(f.db.Create(&exits).Error)
	rules := repository.NewRuleRepository(f.db)
	mustSucceed(rules.RecordExitRelayTraffic(exits[0].ID, "relay-tunnel-1", 30, 40, 30, 40, 100))
	mustSucceed(rules.RecordExitRelayTraffic(exits[1].ID, "relay-tunnel-4", 0, 0, 0, 0, 100))
	mustSucceed(rules.RecordExitRelayTraffic(exits[2].ID, "relay-tunnel-5", 70, 80, 70, 80, 100))
	mustSucceed(rules.RecordExitRelayTraffic(exits[3].ID, "relay-tunnel-6", 5, 5, 5, 5, 100))
	tunnels := []model.GostTunnel{
		{Name: "archived link", EntryNodeID: entry, ExitNodeID: shared, ExitRuleID: &exits[0].ID},
		{Name: "existing link sharing the exit", EntryNodeID: entry, ExitNodeID: shared, ExitRuleID: &exits[0].ID},
		{Name: "another link sharing the exit", EntryNodeID: entry, ExitNodeID: shared, ExitRuleID: &exits[0].ID},
		{Name: "legacy link", EntryNodeID: entry, ExitNodeID: shared},
		{Name: "unsampled link", EntryNodeID: entry, ExitNodeID: unsampled},
	}
	mustSucceed(f.db.Create(&tunnels).Error)
	tunnelRepo := repository.NewTunnelRepository(f.db)
	mustSucceed(tunnelRepo.RecordRelayTraffic(tunnels[0].ID, 10, 20, 10, 20, 100))
	mustSucceed(tunnelRepo.Delete(tunnels[0].ID))
	mustSucceed(tunnelRepo.RecordRelayTraffic(tunnels[3].ID, 1, 2, 1, 2, 100))
	nodeRepo := repository.NewNodeRepository(f.db)
	mustSucceed(nodeRepo.UpdateStats(shared, 3, 4))
	mustSucceed(nodeRepo.UpdateStats(entry, 7, 8))
	// Archived counters stay attributed to the node after an exit is removed.
	mustSucceed(f.db.Delete(&exits[3]).Error)

	want := map[uint]uint64{shared: 120, entry: 15, zero: 0, capped: 66}
	check := func(got nodeTrafficResponse) {
		t.Helper()
		used, measured := want[got.ID]
		if (got.CurrentPeriodUsedBytes != nil) != measured {
			t.Fatalf("node %q reported usage: %t, want %t", got.Name, got.CurrentPeriodUsedBytes != nil, measured)
		}
		if measured && *got.CurrentPeriodUsedBytes != used {
			t.Fatalf("node %q usage = %d, want %d", got.Name, *got.CurrentPeriodUsedBytes, used)
		}
	}
	page := handlerData[handlerPage[nodeTrafficResponse]](t, f.request(t, "GET", "/nodes", "", 200))
	if len(page.List) != len(nodes) {
		t.Fatalf("expected %d nodes, got %d", len(nodes), len(page.List))
	}
	for _, item := range page.List {
		check(item)
	}
	for _, node := range nodes {
		check(handlerData[nodeTrafficResponse](t, f.request(t, "GET", fmt.Sprintf("/nodes/%d", node.ID), "", 200)))
	}
}

type handlerReleaseTransport func(*http.Request) (*http.Response, error)

func (call handlerReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return call(req)
}

func TestNodeHTTPBridgeOperations(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	f.registerSettings()
	const nftJSON = `{"nftables":[{"add":{"chain":{"name":"legacy"}}}]}`
	control := &handlerNodeControl{
		nft:      nftJSON,
		failures: make(map[string]codes.Code),
		admin: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "GET" || req.URL.Path != "/config" {
				t.Errorf("unexpected Gost admin request: %s %s", req.Method, req.URL)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"services":[{"name":"visible-service","addr":":9000"}]}`)
		}),
	}
	node := f.enrollFixtureNode(t, "bridge", control)
	path := fmt.Sprintf("/nodes/%d", node.ID)

	cfg := handlerData[gost.GostConfig](t, f.request(t, "GET", path+"/config", "", 200))
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "visible-service" {
		t.Fatalf("returned Gost config: %+v", cfg)
	}
	f.request(t, "GET", "/nodes/999/config", "", 500)
	for _, item := range []struct{ method, suffix string }{
		{"GET", "/gost/service"}, {"POST", "/gost/restart"}, {"GET", "/gost/logs"},
		{"GET", "/nft"},
	} {
		f.request(t, item.method, "/nodes/0"+item.suffix, "", 400)
		f.request(t, item.method, "/nodes/999"+item.suffix, "", 404)
	}
	for _, method := range []string{"GET", "POST"} {
		suffix := "/gost/service"
		if method == "POST" {
			suffix = "/gost/restart"
		}
		state := handlerData[nodegrpc.GostServiceStatus](t, f.request(t, method, path+suffix, "", 200))
		if state.GetUnit() != "gost.service" || state.GetActiveState() != "active" {
			t.Fatalf("%s Gost service status: %+v", method, &state)
		}
	}
	logs := handlerData[nodegrpc.GostLogsReply](t, f.request(t, "GET", path+"/gost/logs", "", 200))
	if entries := logs.GetEntries(); len(entries) != 1 || entries[0].GetMessage() != "gost started" {
		t.Fatalf("bridge log: %+v", &logs)
	}
	for _, test := range []struct {
		rpcCode codes.Code
		http    int
	}{
		{codes.InvalidArgument, 400}, {codes.ResourceExhausted, 400}, {codes.Unimplemented, 501},
		{codes.FailedPrecondition, 409}, {codes.NotFound, 409}, {codes.PermissionDenied, 409},
		{codes.DeadlineExceeded, 504}, {codes.Unavailable, 502}, {codes.Unauthenticated, 502}, {codes.Internal, 500},
	} {
		control.reject(nodegrpc.V1_GetGostServiceStatus_FullMethodName, test.rpcCode)
		f.request(t, "GET", path+"/gost/service", "", test.http)
	}
	control.reject(nodegrpc.V1_RestartGostService_FullMethodName, codes.FailedPrecondition)
	f.request(t, "POST", path+"/gost/restart", "", 409)
	control.reject(nodegrpc.V1_GetGostLogs_FullMethodName, codes.Unimplemented)
	f.request(t, "GET", path+"/gost/logs", "", 501)

	f.request(t, "GET", "/nodes/0/nft", "", 400)
	read := handlerData[nodeNftConfig](t, f.request(t, "GET", path+"/nft", "", 200))
	if read.Config != nftJSON || read.Table != nodegrpc.NftTableName(uint64(node.ID)) || read.Live {
		t.Fatalf("bridge returned an unexpected saved nftables config: %+v", read)
	}
	const liveNft = `{"nftables":[]}`
	control.mu.Lock()
	control.nftLive = liveNft
	control.mu.Unlock()
	read = handlerData[nodeNftConfig](t, f.request(t, "GET", path+"/nft", "", 200))
	if read.Config != liveNft || !read.Live {
		t.Fatalf("bridge returned an unexpected live nftables config: %+v", read)
	}
	control.reject(nodegrpc.V1_GetNftConfig_FullMethodName, codes.Unimplemented)
	f.request(t, "GET", path+"/nft", "", 501)
	control.mu.Lock()
	restarts := control.restartCall
	control.mu.Unlock()
	if restarts != 1 {
		t.Fatalf("node restarted %d times, want 1", restarts)
	}
	audit := handlerData[handlerPage[model.OperationLog]](t, f.request(t, "GET", "/logs?resource_type=node", "", 200))
	if audit.Total != 1 || len(audit.List) != 1 || audit.List[0].ResourceID != node.ID || audit.List[0].Details != "重启 Gost 服务" {
		t.Fatalf("only successful node operations should appear in the audit log: %+v", audit)
	}
}

func TestNodeHTTPUpgradeLifecycle(t *testing.T) {
	t.Setenv("GH_PROXY", "")
	version := buildinfo.Version
	buildinfo.Version = "0.1.2-abcdef1"
	t.Cleanup(func() { buildinfo.Version = version })
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	control := &handlerNodeControl{failures: make(map[string]codes.Code)}
	node := f.enrollFixtureNode(t, "upgrade-node", control)
	path := fmt.Sprintf("/nodes/%d/upgrade", node.ID)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var lookupCount int
	http.DefaultTransport = handlerReleaseTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.github.com/repos/go-gost/gost/releases/latest" {
			t.Errorf("unexpected release lookup: %s", req.URL)
			return nil, fmt.Errorf("unexpected release lookup: %s", req.URL)
		}
		lookupCount++
		statusCode, body := http.StatusOK, `{"tag_name":"v3.1.0"}`
		switch lookupCount {
		case 1:
			body = `{"tag_name":"v3.0.0"}`
		case 2:
			statusCode, body = http.StatusServiceUnavailable, "release unavailable"
		}
		return &http.Response{StatusCode: statusCode, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	policy := `{"auto_upgrade_daemon":true}`
	f.request(t, "GET", "/nodes/999/upgrade", "", 404)
	f.request(t, "PUT", "/nodes/999/upgrade", policy, 404)
	f.request(t, "GET", "/nodes/0/upgrade", "", 400)
	f.request(t, "PUT", "/nodes/0/upgrade", policy, 400)
	f.request(t, "POST", "/nodes/0/upgrade/gost", "", 400)
	f.request(t, "PUT", path, `{"auto_upgrade_gost":true}`, 400)
	f.request(t, "PUT", path, policy, 200)
	f.request(t, "PUT", path, `{"auto_upgrade_gost":true,"auto_upgrade_daemon":true}`, 200)
	view := handlerData[service.NodeUpgradeView](t, f.request(t, "GET", path, "", 200))
	if view.AutoUpgradeGost || !view.AutoUpgradeDaemon || !view.Available || view.GostUpdateAvailable ||
		view.LatestGostVersion != "" || view.GostVersion != "3.0.0" || !view.DaemonUpdateAvailable {
		t.Fatalf("upgrade view: %+v", view)
	}
	f.request(t, "POST", path+"/unknown", "", 400)
	state := handlerData[nodegrpc.NodeUpgradeStatus](t, f.request(t, "POST", path+"/daemon", "", 200))
	control.mu.Lock()
	started := control.upgrade
	control.mu.Unlock()
	if state.GetState() != "queued" || state.GetTargetVersion() != buildinfo.Version ||
		started.GetVersion() != buildinfo.Version || started.GetComponent() != "daemon" {
		t.Fatalf("upgrade request: state %+v, RPC %+v", &state, started)
	}
	if lookupCount != 0 {
		t.Fatalf("status or daemon upgrade performed %d release lookups", lookupCount)
	}
	f.request(t, "POST", path+"/gost", "", 409)
	f.request(t, "POST", path+"/gost", "", 502)
	state = handlerData[nodegrpc.NodeUpgradeStatus](t, f.request(t, "POST", path+"/gost", "", 200))
	control.mu.Lock()
	started = control.upgrade
	control.mu.Unlock()
	if state.GetState() != "queued" || state.GetTargetVersion() != "v3.1.0" ||
		started.GetVersion() != "v3.1.0" || started.GetComponent() != "gost" || lookupCount != 3 {
		t.Fatalf("manual Gost upgrade: state %+v, RPC %+v, lookups %d", &state, started, lookupCount)
	}
}
