package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type tunnelProbeNode struct {
	nodegrpc.UnimplementedV1Server
	start func() (*nodegrpc.TunnelProbeEndpoint, error)
	probe func(*nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error)
}

func (n tunnelProbeNode) StartTunnelProbe(context.Context, *emptypb.Empty) (*nodegrpc.TunnelProbeEndpoint, error) {
	return n.start()
}

func (n tunnelProbeNode) ProbeTunnel(_ context.Context, req *nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error) {
	return n.probe(req)
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
	}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}

	address, token := "127.0.0.1:43210", bytes.Repeat([]byte{0x7f}, 32)
	startResult, probeResult := make(chan error, 1), make(chan error, 1)
	starts, probes := make(chan struct{}, 1), make(chan *nodegrpc.TunnelProbeRequest, 1)
	panel := startTestReportServer(t, f.db)
	startTestReportStream(t, t.Context(), panel, exit, nodegrpc.NewDispatcher(tunnelProbeNode{
		start: func() (*nodegrpc.TunnelProbeEndpoint, error) {
			starts <- struct{}{}
			return &nodegrpc.TunnelProbeEndpoint{Address: address, Token: token}, <-startResult
		},
	}, nodegrpc.V1_StartTunnelProbe_FullMethodName))
	startTestReportStream(t, t.Context(), panel, entry, nodegrpc.NewDispatcher(tunnelProbeNode{
		probe: func(req *nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error) {
			probes <- req
			return &nodegrpc.TunnelProbeReply{RoundTripMs: 23}, <-probeResult
		},
	}, nodegrpc.V1_ProbeTunnel_FullMethodName))

	path := fmt.Sprintf("/tunnels/%d/test", tunnel.ID)
	for _, tt := range []struct {
		name       string
		prepare    error
		probe      error
		wantHTTP   int
		wantDetail string
	}{
		{name: "round trip", wantHTTP: http.StatusOK},
		{name: "exit unavailable", prepare: status.Error(codes.Unavailable, "could not listen"), wantHTTP: http.StatusBadGateway, wantDetail: "无法在出口节点启动测试，请检查节点的 daemon 连接和运行状态"},
		{name: "old exit daemon", prepare: status.Error(codes.Unimplemented, "old daemon"), wantHTTP: http.StatusNotImplemented, wantDetail: "请升级出口节点的 yagp-daemon 后再测试隧道"},
		{name: "old entry daemon", probe: status.Error(codes.Unimplemented, "old daemon"), wantHTTP: http.StatusNotImplemented, wantDetail: "请升级入口节点的 yagp-daemon 后再测试隧道"},
		{name: "chain missing", probe: status.Error(codes.NotFound, "chain not found"), wantHTTP: http.StatusBadRequest, wantDetail: "隧道未启动或链路不存在"},
		{name: "chain invalid", probe: status.Error(codes.FailedPrecondition, "chain invalid"), wantHTTP: http.StatusConflict, wantDetail: "入口节点无法使用 Gost 隧道链，请检查节点的连接和隧道配置"},
		{name: "local Gost failed", probe: status.Error(codes.Internal, "could not start Gost"), wantHTTP: http.StatusBadGateway, wantDetail: "入口节点无法准备 Gost 测试，请检查 Gost 安装和服务状态"},
		{name: "timed out", probe: status.Error(codes.DeadlineExceeded, "probe timeout"), wantHTTP: http.StatusGatewayTimeout, wantDetail: "隧道转发测试超时，请检查两节点之间的连接和出口 Relay 的运行状态"},
		{name: "wrong response", probe: status.Error(codes.DataLoss, "probe mismatch"), wantHTTP: http.StatusBadGateway, wantDetail: "隧道返回的测试数据校验失败，请检查入口转发链和出口 Relay"},
		{name: "relay unavailable", probe: status.Error(codes.Unavailable, "probe closed"), wantHTTP: http.StatusBadGateway, wantDetail: "测试数据未能通过隧道往返，请检查入口节点和出口 Relay 的运行状态"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startResult <- tt.prepare
			if tt.prepare == nil {
				probeResult <- tt.probe
			}
			reply := f.request(t, http.MethodPost, path, "", tt.wantHTTP)
			if tt.wantHTTP == http.StatusOK {
				result := handlerData[tunnelTestResult](t, reply)
				if !result.Success || result.LatencyMS != 23 {
					t.Fatalf("probe result = %+v", result)
				}
			} else if reply.Message != tt.wantDetail {
				t.Fatalf("message = %q, want %q", reply.Message, tt.wantDetail)
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
}
