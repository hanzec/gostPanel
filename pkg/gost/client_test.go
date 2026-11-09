package gost

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"yagp/internal/streamtest"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type controlCall struct {
	method   string
	request  proto.Message
	response proto.Message
	err      error
}

func controlFixture(t *testing.T) (*Client, chan<- controlCall) {
	t.Helper()
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	pending := make(chan controlCall, 10)
	session := streamtest.Open(t, func(_ context.Context, method string, body []byte) ([]byte, error) {
		select {
		case call := <-pending:
			got := call.request.ProtoReflect().New().Interface()
			if err := proto.Unmarshal(body, got); err != nil || method != call.method || !sameControlRequest(got, call.request) {
				t.Errorf("request %s %v, want %s %v", method, got, call.method, call.request)
				return nil, status.Error(codes.InvalidArgument, "unexpected request")
			}
			if call.err != nil {
				return nil, call.err
			}
			return proto.Marshal(call.response)
		default:
			t.Errorf("unexpected request %s", method)
			return nil, status.Error(codes.InvalidArgument, "unexpected request")
		}
	})
	config := &Config{NodeID: 1, Username: "gost-test", Password: "fixture-password"}
	t.Cleanup(RegisterNodeStream(config.NodeID, 1, config.Username, config.Password, session))
	t.Cleanup(func() {
		if len(pending) != 0 {
			t.Errorf("%d expected node calls were not made", len(pending))
		}
	})
	return NewClient(config), pending
}

func sameControlRequest(got, want proto.Message) bool {
	if actual, ok := got.(*nodegrpc.CommandRequest); ok {
		expected, ok := want.(*nodegrpc.CommandRequest)
		if !ok || actual.Method != expected.Method || actual.Path != expected.Path {
			return false
		}
		if len(actual.Body) == 0 || len(expected.Body) == 0 {
			return len(actual.Body) == len(expected.Body)
		}
		var a, b any
		return json.Unmarshal(actual.Body, &a) == nil && json.Unmarshal(expected.Body, &b) == nil && reflect.DeepEqual(a, b)
	}
	return proto.Equal(got, want)
}

func gostCommand(method, path, body string, code int, reply string) controlCall {
	return controlCall{
		method:   nodegrpc.V1_Execute_FullMethodName,
		request:  &nodegrpc.CommandRequest{Method: method, Path: path, Body: []byte(body)},
		response: &nodegrpc.CommandReply{StatusCode: int32(code), Body: []byte(reply)},
	}
}

func TestGostResourceLifecycle(t *testing.T) {
	client, calls := controlFixture(t)
	service := func() error {
		_, err := client.EnsureService(&ServiceConfig{Name: "demo"})
		return err
	}
	for _, resource := range []struct {
		name, payload string
		create        func() error
		delete        func(string) error
	}{
		{"services", `{"name":"demo","addr":""}`, service, client.DeleteService},
		{"chains", `{"name":"demo"}`, func() error { return client.CreateChain(&ChainConfig{Name: "demo"}) }, client.DeleteChain},
		{"limiters", `{"name":"demo"}`, func() error { return client.CreateLimiter(&LimiterConfig{Name: "demo"}) }, client.DeleteLimiter},
		{"climiters", `{"name":"demo"}`, func() error { return client.CreateCLimiter(&CLimiterConfig{Name: "demo"}) }, client.DeleteCLimiter},
		{"rlimiters", `{"name":"demo"}`, func() error { return client.CreateRLimiter(&RLimiterConfig{Name: "demo"}) }, client.DeleteRLimiter},
	} {
		t.Run(resource.name, func(t *testing.T) {
			base := "/config/" + resource.name
			missing := gostCommand("GET", base+"/demo", "", http.StatusOK, `{"data":null}`)
			existing := gostCommand("GET", base+"/demo", "", http.StatusOK, `{"data":{"name":"demo"}}`)
			lookupError := gostCommand("GET", base+"/demo", "", http.StatusBadGateway, "lookup failed")
			create := gostCommand("POST", base, resource.payload, http.StatusCreated, "{}")
			remove := gostCommand("DELETE", base+"/demo", "", http.StatusNoContent, "")
			for _, scenario := range []struct {
				name      string
				run       func() error
				calls     []controlCall
				wantError string
			}{
				{"create when missing", resource.create, []controlCall{missing, create}, ""},
				{"keep existing", resource.create, []controlCall{existing}, ""},
				{"delete existing", func() error { return resource.delete("demo") }, []controlCall{existing, remove}, ""},
				{"ignore missing deletion", func() error { return resource.delete("demo") }, []controlCall{missing}, ""},
				{"failed create lookup", resource.create, []controlCall{lookupError}, "502"},
				{"failed delete lookup", func() error { return resource.delete("demo") }, []controlCall{lookupError}, "502"},
				{"rejected create", resource.create, []controlCall{missing, gostCommand("POST", base, resource.payload, http.StatusConflict, "port is in use")}, "port is in use"},
				{"rejected delete", func() error { return resource.delete("demo") }, []controlCall{existing, gostCommand("DELETE", base+"/demo", "", http.StatusForbidden, "cannot remove")}, "cannot remove"},
				{"interrupted create", resource.create, []controlCall{missing, {method: create.method, request: create.request, err: status.Error(codes.Unavailable, "connection lost")}}, "connection lost"},
				{"interrupted delete", func() error { return resource.delete("demo") }, []controlCall{existing, {method: remove.method, request: remove.request, err: status.Error(codes.Unavailable, "connection lost")}}, "connection lost"},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					for _, call := range scenario.calls {
						calls <- call
					}
					err := scenario.run()
					if scenario.wantError == "" && err != nil || scenario.wantError != "" && (err == nil || !strings.Contains(err.Error(), scenario.wantError)) {
						t.Fatalf("result = %v, expected error containing %q", err, scenario.wantError)
					}
				})
			}
		})
	}
}

func TestGostConfigAndForwardingBuilders(t *testing.T) {
	client, calls := controlFixture(t)
	path := "/config"
	calls <- gostCommand("GET", path, "", 200, `{"services":[{"name":"live","addr":":443"}]}`)
	cfg, err := client.GetConfig()
	if err != nil || len(cfg.Services) != 1 || cfg.Services[0].Addr != ":443" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	calls <- gostCommand("GET", path, "", 200, "{}")
	if err := client.HealthCheck(); err != nil {
		t.Fatal(err)
	}
	calls <- gostCommand("GET", path, "", 503, "starting")
	if err := client.HealthCheck(); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("health = %v", err)
	}
	for _, reply := range []controlCall{gostCommand("GET", path, "", 503, "starting"), gostCommand("GET", path, "", 200, "bad JSON")} {
		calls <- reply
		if _, err := client.GetConfig(); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	calls <- gostCommand("POST", path+"?format=yaml", "", 200, "")
	if err := client.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	calls <- gostCommand("POST", path+"?format=yaml", "", 500, "read only")
	if err := client.SaveConfig(); err == nil || !strings.Contains(err.Error(), "read only") {
		t.Fatalf("save = %v", err)
	}
	unregistered := NewClient(&Config{})
	for _, call := range []func() error{unregistered.HealthCheck, unregistered.SaveConfig, func() error {
		_, err := unregistered.GetConfig()
		return err
	}} {
		if err := call(); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("unregistered node: %v", err)
		}
	}
	offline := NewClient(&Config{NodeID: 99, Username: "a", Password: "b"})
	if err := offline.HealthCheck(); status.Code(err) != codes.Unavailable {
		t.Fatalf("offline node: %v", err)
	}
	targets := []string{"127.0.0.1:80", "[::1]:81"}
	for protocol, build := range map[string]func(string, int, []string, string) *ServiceConfig{
		"tcp": BuildTCPForwardService, "udp": BuildUDPForwardService,
	} {
		svc := build("demo", 443, targets, "")
		if svc.Addr != ":443" || svc.Handler.Type != "forward" || svc.Listener.Type != protocol ||
			svc.Forwarder.Selector.Strategy != "round" || len(svc.Forwarder.Nodes) != 2 ||
			svc.Forwarder.Nodes[1].Addr != targets[1] {
			t.Fatalf("unexpected %s forwarding config: %+v", protocol, svc)
		}
	}
}

func TestGostTypedControlCalls(t *testing.T) {
	ctx := context.Background()
	client, calls := controlFixture(t)
	listener := &nodegrpc.ListenerCheckRequest{Protocol: "tcp", Ip: "::1", Port: 16142}
	available := &nodegrpc.ListenerCheckReply{Available: true}
	calls <- controlCall{method: nodegrpc.V1_CheckListener_FullMethodName, request: listener, response: available}
	if got, err := client.CheckListener(ctx, listener.Protocol, listener.Ip, listener.Port); err != nil || !proto.Equal(got, available) {
		t.Fatalf("check listener = %v, %v", got, err)
	}
	state := &nodegrpc.GostServiceStatus{Unit: "yagp-gost.service", ActiveState: "active"}
	calls <- controlCall{method: nodegrpc.V1_GetGostServiceStatus_FullMethodName, request: &emptypb.Empty{}, response: state}
	if got, err := client.GostServiceStatus(ctx); err != nil || !proto.Equal(got, state) {
		t.Fatalf("service status = %v, %v", got, err)
	}
	calls <- controlCall{method: nodegrpc.V1_RestartGostService_FullMethodName, request: &emptypb.Empty{}, response: state}
	if got, err := client.RestartGostService(ctx); err != nil || !proto.Equal(got, state) {
		t.Fatalf("service restart = %v, %v", got, err)
	}
	logs := &nodegrpc.GostLogsReply{Entries: []*nodegrpc.GostLogEntry{{Message: "ready"}}}
	calls <- controlCall{method: nodegrpc.V1_GetGostLogs_FullMethodName, request: &nodegrpc.GostLogsRequest{Lines: 100}, response: logs}
	if got, err := client.GostLogs(ctx); err != nil || !proto.Equal(got, logs) {
		t.Fatalf("service logs = %v, %v", got, err)
	}
	nft := &nodegrpc.NftConfig{Config: `{"nftables":[]}`}
	calls <- controlCall{method: nodegrpc.V1_GetNftConfig_FullMethodName, request: &emptypb.Empty{}, response: nft}
	if got, err := client.GetNftConfig(ctx); err != nil || !proto.Equal(got, nft) {
		t.Fatalf("read nft = %v, %v", got, err)
	}
	calls <- controlCall{method: nodegrpc.V1_ApplyNftConfig_FullMethodName, request: nft, response: nft}
	if got, err := client.ApplyNftConfig(ctx, nft.Config); err != nil || !proto.Equal(got, nft) {
		t.Fatalf("apply nft = %v, %v", got, err)
	}
	upgrade := &nodegrpc.NodeUpgradeStatus{Supported: true, GostVersion: "3.2.6"}
	calls <- controlCall{method: nodegrpc.V1_GetUpgradeStatus_FullMethodName, request: &emptypb.Empty{}, response: upgrade}
	if got, err := client.GetUpgradeStatus(ctx); err != nil || !proto.Equal(got, upgrade) {
		t.Fatalf("upgrade status = %v, %v", got, err)
	}
	calls <- controlCall{method: nodegrpc.V1_StartUpgrade_FullMethodName, request: &nodegrpc.NodeUpgradeRequest{Component: "gost", Version: "v3.3.3"}, response: upgrade}
	if got, err := client.StartUpgrade(ctx, "gost", "v3.3.3"); err != nil || !proto.Equal(got, upgrade) {
		t.Fatalf("start upgrade = %v, %v", got, err)
	}
	quota := &nodegrpc.TrafficQuotaRequest{LimitBytes: 200, UsedBytes: 100, WindowStart: 50, TcpPorts: []uint32{80}, UdpPorts: []uint32{53}}
	calls <- controlCall{method: nodegrpc.V1_SyncTrafficQuota_FullMethodName, request: quota, response: &nodegrpc.TrafficQuotaReply{UsedBytes: 101}}
	if used, err := client.SyncTrafficQuota(ctx, 200, 100, 50, []uint32{80}, []uint32{53}); err != nil || used != 101 {
		t.Fatalf("sync quota = %v, %v", used, err)
	}
	zero := uint64(0)
	quota.CalibratedUsedBytes = &zero
	calls <- controlCall{method: nodegrpc.V1_SyncTrafficQuota_FullMethodName, request: quota, response: &nodegrpc.TrafficQuotaReply{UsedBytes: 101}}
	if _, err := client.CalibrateTrafficQuota(ctx, quota); status.Code(err) != codes.Unimplemented {
		t.Fatalf("legacy response to quota calibration = %v", err)
	}
	calls <- controlCall{method: nodegrpc.V1_SyncTrafficQuota_FullMethodName, request: quota, response: &nodegrpc.TrafficQuotaReply{Calibrated: true}}
	if used, err := client.CalibrateTrafficQuota(ctx, quota); err != nil || used != 0 {
		t.Fatalf("calibrate quota = %v, %v", used, err)
	}
	unregistered := NewClient(&Config{})
	if _, err := unregistered.GostServiceStatus(ctx); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unregistered service status = %v", err)
	}
	if _, err := unregistered.GetUpgradeStatus(ctx); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unregistered upgrade status = %v", err)
	}
	if _, err := unregistered.StartUpgrade(ctx, "gost", "v3.3.3"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unregistered upgrade = %v", err)
	}
}

func TestNodeStreamReplacementKeepsTheCurrentConnection(t *testing.T) {
	const id = 42
	previous := streamtest.Open(t, nil)
	removePrevious := RegisterNodeStream(id, 1, "node", "previous", previous)
	defer removePrevious()
	current := streamtest.Open(t, func(context.Context, string, []byte) ([]byte, error) {
		return proto.Marshal(&nodegrpc.CommandReply{StatusCode: http.StatusOK})
	})
	defer RegisterNodeStream(id, 1, "node", "current", current)()
	select {
	case <-previous.Done():
	default:
		t.Fatal("replaced stream remained open")
	}
	removePrevious()
	if err := NewClient(&Config{NodeID: id, Username: "node", Password: "previous"}).HealthCheck(); status.Code(err) != codes.Unavailable {
		t.Fatalf("stale credentials: %v", err)
	}
	client := NewClient(&Config{NodeID: id, Username: "node", Password: "current"})
	if err := client.HealthCheck(); err != nil {
		t.Fatalf("stale cleanup removed the current connection: %v", err)
	}
	DisconnectNode(id)
	if err := client.HealthCheck(); status.Code(err) != codes.Unavailable {
		t.Fatalf("disconnected node: %v", err)
	}
}

func TestNodeStreamRejectsDelayedOlderEnrollment(t *testing.T) {
	const id = 43
	// Both generations have the same local Gost credentials after re-enrollment.
	current := streamtest.Open(t, func(context.Context, string, []byte) ([]byte, error) {
		return proto.Marshal(&nodegrpc.CommandReply{StatusCode: http.StatusOK})
	})
	unregister := RegisterNodeStream(id, 2, "node", "password", current)
	if unregister == nil {
		t.Fatal("current enrollment was rejected")
	}
	defer unregister()

	delayed := streamtest.Open(t, nil)
	if remove := RegisterNodeStream(id, 1, "node", "password", delayed); remove != nil {
		remove()
		t.Fatal("delayed handshake replaced the newer enrollment")
	}
	client := NewClient(&Config{NodeID: id, Username: "node", Password: "password"})
	if err := client.HealthCheck(); err != nil {
		t.Fatalf("newer enrollment did not retain its usable connection: %v", err)
	}
}
