package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type tunnelProbeNode struct {
	nodegrpc.UnimplementedV1Server
	start      func() (*nodegrpc.TunnelProbeEndpoint, error)
	probe      func(*nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error)
	execute    func(*nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error)
	gostStatus func() (*nodegrpc.GostServiceStatus, error)
}

func (n tunnelProbeNode) StartTunnelProbe(context.Context, *emptypb.Empty) (*nodegrpc.TunnelProbeEndpoint, error) {
	return n.start()
}

func (n tunnelProbeNode) ProbeTunnel(_ context.Context, req *nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error) {
	return n.probe(req)
}

func (n tunnelProbeNode) Execute(_ context.Context, req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	return n.execute(req)
}

func (n tunnelProbeNode) GetGostServiceStatus(context.Context, *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	return n.gostStatus()
}

func TestTunnelProbeHTTPUsesSavedChainAndNodeSessions(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	entry := model.GostNode{Name: "entry", Status: model.NodeStatusOnline, Username: "entry", Password: "entry-test-password"}
	exit := model.GostNode{Name: "exit", Status: model.NodeStatusOnline, Username: "exit", Password: "exit-test-password"}
	for _, node := range []*model.GostNode{&entry, &exit} {
		if err := f.db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	tunnel := model.GostTunnel{
		Name: "test link", EntryNodeID: entry.ID, ExitNodeID: exit.ID,
		Status: model.TunnelStatusRunning, ChainID: "tunnel-1-chain",
		ServiceID: "relay-tunnel-1", Protocol: "tcp", RelayPort: 8443,
	}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}

	address, token := "127.0.0.1:43210", bytes.Repeat([]byte{0x7f}, 32)
	startResult, probeResult := make(chan error, 1), make(chan error, 1)
	starts, probes := make(chan struct{}, 1), make(chan *nodegrpc.TunnelProbeRequest, 1)
	configReads := make(chan *nodegrpc.CommandRequest, 1)
	type exitRelayConfig struct {
		state      string
		admissions []gost.AdmissionConfig
	}
	relayConfig := make(chan exitRelayConfig, 1)
	serviceChecks := make(chan struct{}, 1)
	panel := startTestReportServer(t, f.db)
	startTestReportStream(t, t.Context(), panel, exit, nodegrpc.NewDispatcher(tunnelProbeNode{
		start: func() (*nodegrpc.TunnelProbeEndpoint, error) {
			starts <- struct{}{}
			return &nodegrpc.TunnelProbeEndpoint{Address: address, Token: token}, <-startResult
		},
		execute: func(req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
			configReads <- req
			exitRelay := <-relayConfig
			if exitRelay.state == "api unavailable" {
				return nil, status.Error(codes.Unavailable, "management API unavailable")
			}
			config := gost.GostConfig{}
			if exitRelay.state != "" {
				config.Services = []gost.ServiceConfig{{
					Name: tunnel.ServiceID, Addr: ":8443", Status: &gost.ServiceStatus{State: exitRelay.state},
					Handler:  &gost.HandlerConfig{Type: "relay", Auth: &gost.AuthConfig{Password: "secret-test-value"}},
					Listener: &gost.ListenerConfig{Type: "tcp"}, Admission: "tunnel-allowlist",
					Admissions: []string{"another-allowlist", "relay-denylist"},
				}}
				config.Admissions = exitRelay.admissions
			}
			body, err := json.Marshal(config)
			return &nodegrpc.CommandReply{StatusCode: http.StatusOK, Body: body}, err
		},
		gostStatus: func() (*nodegrpc.GostServiceStatus, error) {
			serviceChecks <- struct{}{}
			return &nodegrpc.GostServiceStatus{ActiveState: "failed"}, nil
		},
	}, nodegrpc.V1_StartTunnelProbe_FullMethodName, nodegrpc.V1_Execute_FullMethodName, nodegrpc.V1_GetGostServiceStatus_FullMethodName))
	startTestReportStream(t, t.Context(), panel, entry, nodegrpc.NewDispatcher(tunnelProbeNode{
		probe: func(req *nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error) {
			probes <- req
			return &nodegrpc.TunnelProbeReply{RoundTripMs: 23}, <-probeResult
		},
	}, nodegrpc.V1_ProbeTunnel_FullMethodName))

	path := fmt.Sprintf("/tunnels/%d/test", tunnel.ID)
	probeError := func(code codes.Code, reason string, metadata map[string]string) error {
		detailed, err := status.New(code, "secret-test-value").WithDetails(&errdetails.ErrorInfo{
			Domain: "yagp.tunnel_probe", Reason: reason, Metadata: metadata,
		})
		if err != nil {
			t.Fatal(err)
		}
		return detailed.Err()
	}
	for _, tt := range []struct {
		name               string
		prepare            error
		probe              error
		wantHTTP           int
		wantDetail         string
		wantStage          string
		checkExit          bool
		relayState         string
		relayAdmissions    []gost.AdmissionConfig
		wantChecks         map[string]string
		wantRelayPolicy    string
		wantRecommendation string
	}{
		{name: "round trip", wantHTTP: http.StatusOK},
		{name: "exit unavailable", prepare: status.Error(codes.Unavailable, "could not listen"), wantHTTP: http.StatusBadGateway, wantDetail: "无法在出口节点启动测试，请检查节点的 daemon 连接和运行状态", wantStage: "exit_listener"},
		{name: "old exit daemon", prepare: status.Error(codes.Unimplemented, "old daemon"), wantHTTP: http.StatusNotImplemented, wantDetail: "请升级出口节点的 yagp-daemon 后再测试隧道", wantStage: "exit_listener"},
		{name: "old entry daemon", probe: status.Error(codes.Unimplemented, "old daemon"), wantHTTP: http.StatusNotImplemented, wantDetail: "请升级入口节点的 yagp-daemon 后再测试隧道", wantStage: "entry_node"},
		{name: "chain missing", probe: status.Error(codes.NotFound, "chain not found"), wantHTTP: http.StatusBadRequest, wantDetail: "隧道未启动或链路不存在", wantStage: "entry_chain"},
		{name: "chain invalid", probe: status.Error(codes.FailedPrecondition, "chain invalid"), wantHTTP: http.StatusConflict, wantDetail: "入口节点无法使用 Gost 隧道链，请检查节点的连接和隧道配置", wantStage: "entry_chain"},
		{name: "local Gost failed", probe: status.Error(codes.Internal, "could not start Gost"), wantHTTP: http.StatusBadGateway, wantDetail: "入口节点无法准备 Gost 测试，请检查 Gost 安装和服务状态", wantStage: "entry_gost_process"},
		{name: "management API failed", probe: probeError(codes.Internal, "entry_gost_api", nil), wantHTTP: http.StatusBadGateway, wantStage: "entry_gost_api"},
		{name: "timed out", probe: status.Error(codes.DeadlineExceeded, "probe timeout"), wantHTTP: http.StatusGatewayTimeout, wantDetail: "隧道转发测试超时，请检查两节点之间的连接和出口 Relay 的运行状态", checkExit: true, relayState: "ready", wantStage: "relay_round_trip"},
		{name: "wrong response", probe: status.Error(codes.DataLoss, "probe mismatch"), wantHTTP: http.StatusBadGateway, wantDetail: "隧道返回的测试数据校验失败，请检查入口转发链和出口 Relay", checkExit: true, relayState: "running", wantStage: "relay_round_trip"},
		{name: "relay unavailable", probe: status.Error(codes.Unavailable, "probe closed"), wantHTTP: http.StatusBadGateway, wantDetail: "测试数据未能通过隧道往返，请检查入口节点和出口 Relay 的运行状态", checkExit: true, wantStage: "exit_relay"},
		{name: "relay failed", probe: probeError(codes.Unavailable, "relay_read", map[string]string{"relay_tcp": "refused"}), wantHTTP: http.StatusBadGateway, checkExit: true, relayState: "failed", wantStage: "exit_relay", wantChecks: map[string]string{"relay_connection": "failed", "exit_relay": "failed"}},
		{name: "exit Gost stopped", probe: status.Error(codes.Unavailable, "probe closed"), wantHTTP: http.StatusBadGateway, checkExit: true, relayState: "api unavailable", wantStage: "exit_relay", wantChecks: map[string]string{"exit_relay": "failed"}},
		{
			name: "relay authentication", probe: probeError(codes.Unavailable, "relay_read", map[string]string{"relay_tcp": "reachable", "gost_error": "relay_authentication"}),
			wantHTTP: http.StatusBadGateway, checkExit: true, relayState: "running", wantStage: "relay_handshake",
			relayAdmissions: []gost.AdmissionConfig{
				{Name: "tunnel-allowlist", Whitelist: true, Matchers: []string{"192.0.2.1", "2001:db8::/48"}},
				{Name: "unrelated-allowlist", Whitelist: true, Matchers: []string{"198.51.100.10"}},
				{Name: "relay-denylist", Matchers: []string{"198.51.100.11"}},
				{Name: "another-allowlist", Whitelist: true, Matchers: []string{"203.0.113.0/24"}},
			},
			wantChecks:      map[string]string{"relay_connection": "passed", "relay_policy": "skipped"},
			wantRelayPolicy: "出口 Relay 启用了来源白名单，本次探测未能确认来源准入结果。当前配置的白名单条目：192.0.2.1、2001:db8::/48、203.0.113.0/24。",
		},
		{
			name: "connection closed with no Relay response", probe: probeError(codes.Unavailable, "relay_read", map[string]string{"relay_tcp": "reachable"}),
			wantHTTP: http.StatusBadGateway, checkExit: true, relayState: "running", wantStage: "relay_round_trip",
			relayAdmissions: []gost.AdmissionConfig{{Name: "tunnel-allowlist", Whitelist: true}},
			wantChecks:      map[string]string{"relay_connection": "passed", "relay_policy": "skipped"},
			wantRelayPolicy: "出口 Relay 启用了来源白名单，本次探测未能确认来源准入结果。当前配置未列出白名单条目。",
		},
		{
			name: "relay target forbidden", probe: probeError(codes.Unavailable, "relay_read", map[string]string{"relay_tcp": "reachable", "gost_error": "relay_target_forbidden"}),
			wantHTTP: http.StatusBadGateway, checkExit: true, relayState: "running", wantStage: "relay_handshake",
			relayAdmissions:    []gost.AdmissionConfig{{Name: "tunnel-allowlist", Whitelist: true, Matchers: []string{"192.0.2.1"}}},
			wantChecks:         map[string]string{"relay_connection": "passed", "relay_policy": "passed", "relay_handshake": "failed"},
			wantRelayPolicy:    "本次探测已通过出口 Relay 的来源白名单检查。当前配置的白名单条目：192.0.2.1。",
			wantRecommendation: "请通过入口使用出口目标策略允许的业务地址验证转发。",
		},
		{
			name: "relay target unreachable", probe: probeError(codes.Unavailable, "relay_read", map[string]string{"gost_error": "relay_target_unreachable"}),
			wantHTTP: http.StatusBadGateway, checkExit: true, relayState: "running", wantStage: "relay_handshake",
			relayAdmissions: []gost.AdmissionConfig{{Name: "tunnel-allowlist", Whitelist: true, Matchers: []string{"192.0.2.1"}}},
			wantChecks:      map[string]string{"relay_policy": "passed", "relay_handshake": "failed"},
			wantRelayPolicy: "本次探测已通过出口 Relay 的来源白名单检查。当前配置的白名单条目：192.0.2.1。",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startResult <- tt.prepare
			if tt.prepare == nil {
				probeResult <- tt.probe
			}
			if tt.checkExit {
				relayConfig <- exitRelayConfig{state: tt.relayState, admissions: tt.relayAdmissions}
			}
			reply := f.request(t, http.MethodPost, path, "", tt.wantHTTP)
			result := handlerData[service.TunnelTestResult](t, reply)
			if tt.wantHTTP == http.StatusOK {
				if !result.Success || result.LatencyMS != 23 {
					t.Fatalf("probe result = %+v", result)
				}
			} else {
				if tt.wantDetail != "" && reply.Message != tt.wantDetail {
					t.Fatalf("message = %q, want %q", reply.Message, tt.wantDetail)
				}
				if result.Success || result.FailureStage != tt.wantStage || result.Recommendation == "" {
					t.Fatalf("probe result = %+v, want failure at %s", result, tt.wantStage)
				}
				if tt.wantRecommendation != "" && result.Recommendation != tt.wantRecommendation {
					t.Errorf("recommendation = %q, want %q", result.Recommendation, tt.wantRecommendation)
				}
				checks := make(map[string]service.TunnelTestDiagnostic)
				for _, check := range result.Diagnostics {
					checks[check.Stage] = check
				}
				for stage, want := range tt.wantChecks {
					if checks[stage].Status != want {
						t.Errorf("stage %s = %q, want %q", stage, checks[stage].Status, want)
					}
				}
				if got := checks["relay_policy"].Message; got != tt.wantRelayPolicy {
					t.Errorf("Relay policy message = %q, want %q", got, tt.wantRelayPolicy)
				}
				body, _ := json.Marshal(reply)
				if bytes.Contains(body, []byte("secret-test-value")) {
					t.Fatal("probe exposed raw log text or credentials")
				}
			}
			select {
			case req := <-configReads:
				if !tt.checkExit || req.GetMethod() != http.MethodGet || req.GetPath() != "/config" {
					t.Fatalf("unexpected follow-up request: %v", req)
				}
			default:
				if tt.checkExit {
					t.Fatal("missing exit Relay inspection")
				}
			}
			select {
			case <-serviceChecks:
				if tt.relayState != "api unavailable" {
					t.Fatal("unexpected exit Gost service inspection")
				}
			default:
				if tt.relayState == "api unavailable" {
					t.Fatal("missing exit Gost service inspection")
				}
			}
			select {
			case <-starts:
			default:
				t.Fatal("the exit node did not start the probe")
			}
			select {
			case req := <-probes:
				if tt.prepare != nil || req.GetChainName() != tunnel.ChainID ||
					req.GetAddress() != address || !bytes.Equal(req.GetToken(), token) {
					t.Fatalf("unexpected probe request: %v", req)
				}
			default:
				if tt.prepare == nil {
					t.Fatal("the entry node did not receive the probe")
				}
			}
		})
	}
	var saved model.GostTunnel
	if err := f.db.First(&saved, tunnel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != tunnel.Status || saved.ChainID != tunnel.ChainID || !saved.UpdatedAt.Equal(tunnel.UpdatedAt) {
		t.Fatalf("the probe modified the saved tunnel: %+v", saved)
	}
}

func TestTunnelProbeHTTPValidatesSavedTunnel(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	entry := model.GostNode{Name: "entry", Status: model.NodeStatusOnline}
	exit := model.GostNode{Name: "exit", Status: model.NodeStatusOnline}
	for _, node := range []*model.GostNode{&entry, &exit} {
		if err := f.db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	tunnel := model.GostTunnel{Name: "test link", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Status: model.TunnelStatusStopped}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, "/tunnels/0/test", "", http.StatusBadRequest)
	f.request(t, http.MethodPost, "/tunnels/999/test", "", http.StatusNotFound)
	path := fmt.Sprintf("/tunnels/%d/test", tunnel.ID)
	check := func(want string) {
		t.Helper()
		if reply := f.request(t, http.MethodPost, path, "", http.StatusBadRequest); reply.Message != want {
			t.Fatalf("message = %q, want %q", reply.Message, want)
		}
	}
	update := func(value any, field string, data any) {
		t.Helper()
		if err := f.db.Model(value).Update(field, data).Error; err != nil {
			t.Fatal(err)
		}
	}
	check("隧道未运行")
	update(&tunnel, "status", model.TunnelStatusRunning)
	check("隧道未启动或链路不存在")
	update(&tunnel, "chain_id", "tunnel-1-chain")
	update(&entry, "status", model.NodeStatusOffline)
	check("入口节点已离线")
	update(&entry, "status", model.NodeStatusOnline)
	update(&exit, "status", model.NodeStatusOffline)
	check("出口节点已离线")
	update(&exit, "status", model.NodeStatusOnline)
	reply := f.request(t, http.MethodPost, path, "", http.StatusBadGateway)
	result := handlerData[service.TunnelTestResult](t, reply)
	if result.FailureStage != "exit_node" {
		t.Fatalf("online node without a session should fail at exit_node: %+v", result)
	}
}
