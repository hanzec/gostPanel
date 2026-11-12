package nodegrpc

import (
	"context"
	"testing"
	"time"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type protocolExchange struct {
	method   string
	request  proto.Message
	response proto.Message
	call     func(context.Context, V1Client) (proto.Message, error)
}

func exchange[Request, Response proto.Message](
	method string,
	call func(V1Client, context.Context, Request, ...grpc.CallOption) (Response, error),
	request Request,
	response Response,
) protocolExchange {
	return protocolExchange{method, request, response, func(ctx context.Context, client V1Client) (proto.Message, error) {
		return call(client, ctx, request)
	}}
}

func TestWebSocketWireRoundTrip(t *testing.T) {
	if V1_Execute_FullMethodName != "/yagp.v1/Execute" {
		t.Fatalf("node control path = %s", V1_Execute_FullMethodName)
	}
	empty := &emptypb.Empty{}
	gostStatus := &GostServiceStatus{Unit: "yagp-gost-42.service", LoadState: "loaded", ActiveState: "active", SubState: "running"}
	config := &NftConfig{Config: "{\"nftables\":[]}"}
	// Carry a newer peer's field through the older protobuf schema in both directions.
	config.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 100, protowire.VarintType), 43))
	liveConfig := &NftConfig{
		Config:     config.GetConfig(),
		Table:      SingleNodeNftTable,
		LiveConfig: `{"nftables":[{"table":{"family":"inet","name":"yagp_quota"}}]}`,
	}
	forwardedReport := &ReportRequest{
		NodeId: 42,
		Observation: &observer.ObserveRequest{Events: []*observer.Event{{
			Kind: "service", Service: "rule-1", Type: "stats", Stats: &observer.Stats{InputBytes: 10, OutputBytes: 20},
		}}},
	}
	calls := []protocolExchange{
		exchange(V1_Execute_FullMethodName, V1Client.Execute,
			&CommandRequest{Method: "POST", Path: "/config/services", Body: []byte("{\"name\":\"test\"}")},
			&CommandReply{StatusCode: 201, Body: []byte("{\"created\":true}")}),
		exchange(V1_CheckListener_FullMethodName, V1Client.CheckListener,
			&ListenerCheckRequest{Protocol: "udp", Ip: "2001:db8::42", Port: 13053},
			&ListenerCheckReply{Reason: "occupied"}),
		exchange(V1_GetObserverEndpoint_FullMethodName, V1Client.GetObserverEndpoint,
			empty, &ObserverEndpoint{Address: "127.0.0.1:10900"}),
		exchange(V1_GetGostServiceStatus_FullMethodName, V1Client.GetGostServiceStatus,
			empty, gostStatus),
		exchange(V1_RestartGostService_FullMethodName, V1Client.RestartGostService,
			empty, gostStatus),
		exchange(V1_GetGostLogs_FullMethodName, V1Client.GetGostLogs,
			&GostLogsRequest{Lines: 2}, &GostLogsReply{Entries: []*GostLogEntry{
				{TimestampUnixMicros: 1730000000000000, Message: "started", Priority: 6},
				{TimestampUnixMicros: 1730000000000001, Message: "connection refused", Priority: 3},
			}}),
		exchange(V1_GetNftConfig_FullMethodName, V1Client.GetNftConfig,
			empty, liveConfig),
		exchange(V1_ApplyNftConfig_FullMethodName, V1Client.ApplyNftConfig,
			config, config),
		exchange(V1_ConfigureTransparentRedirect_FullMethodName, V1Client.ConfigureTransparentRedirect,
			&TransparentRedirectRequest{
				Action: TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY,
				Owner:  "rule-42", Port: 12345, RelayHost: "192.0.2.42", Mark: 100, DockerAddress: "172.17.0.1",
				RequireActive: true,
			},
			&TransparentRedirectReply{Mark: 100, DockerAddress: "172.17.0.1", Active: true}),
		exchange(V1_SyncTrafficQuota_FullMethodName, V1Client.SyncTrafficQuota,
			&TrafficQuotaRequest{LimitBytes: 1000, UsedBytes: 123, WindowStart: 456, TcpPorts: []uint32{80, 443}, UdpPorts: []uint32{53}},
			&TrafficQuotaReply{UsedBytes: 200}),
		exchange(V1_GetUpgradeStatus_FullMethodName, V1Client.GetUpgradeStatus, empty,
			&NodeUpgradeStatus{Supported: true, GostVersion: "v3.3.2", DaemonVersion: "0.1.99-abc1234", Component: "gost",
				TargetVersion: "v3.3.3", State: "failed", Error: "restart failed"}),
		exchange(V1_StartUpgrade_FullMethodName, V1Client.StartUpgrade,
			&NodeUpgradeRequest{Component: "gost", Version: "v3.3.3"},
			&NodeUpgradeStatus{Supported: true, Component: "gost", TargetVersion: "v3.3.3", State: "queued"}),
		exchange(V1_Report_FullMethodName, V1Client.Report, forwardedReport, &observer.ObserveReply{Ok: true}),
		exchange(V1_GetNodeConfig_FullMethodName, V1Client.GetNodeConfig, empty,
			&NodeConfig{ConfigYaml: []byte("node_id: 42"), NftTemplate: config.GetConfig()}),
		exchange(V1_UpdateNodeIPs_FullMethodName, V1Client.UpdateNodeIPs,
			&NodeIPsRequest{NodeId: 42, Ipv4: "203.0.113.42", Ipv6: "2001:db8::42"}, empty),
	}
	byMethod := make(map[string]protocolExchange, len(calls))
	for _, call := range calls {
		byMethod[call.method] = call
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stream := openRawSession(t, ctx, func(stream *WebSocketStream) error {
		return NewSession(stream, func(_ context.Context, method string, payload []byte) ([]byte, error) {
			call, ok := byMethod[method]
			if !ok {
				return nil, status.Errorf(codes.Unimplemented, "unknown operation: %s", method)
			}
			request := call.request.ProtoReflect().New().Interface()
			if err := proto.Unmarshal(payload, request); err != nil || !proto.Equal(request, call.request) {
				return nil, status.Errorf(codes.InvalidArgument, "unexpected wire request for %s", method)
			}
			return proto.Marshal(call.response)
		}).Run()
	})
	session := NewSession(stream, nil)
	t.Cleanup(session.Close)
	go func() { _ = session.Run() }()
	client := NewV1Client(session)
	for _, call := range calls {
		t.Run(call.method, func(t *testing.T) {
			reply, err := call.call(ctx, client)
			if err != nil || !proto.Equal(reply, call.response) {
				t.Fatalf("wire reply = %v, %v, want %v", reply, err, call.response)
			}
		})
	}
	if err := session.Invoke(ctx, "/unsupported", empty, empty); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unsupported operation returned %v", err)
	}

	// Run the same operations through the server-side dispatcher. A valid payload
	// reaches the typed method, while a malformed payload is rejected before it.
	methods := make([]string, 0, len(calls))
	for _, call := range calls {
		methods = append(methods, call.method)
	}
	defaultStream := openRawSession(t, ctx, func(stream *WebSocketStream) error {
		return NewSession(stream, NewDispatcher(UnimplementedV1Server{}, methods...)).Run()
	})
	defaults := NewSession(defaultStream, nil)
	t.Cleanup(defaults.Close)
	go func() { _ = defaults.Run() }()
	malformed := new(StreamFrame)
	malformed.ProtoReflect().SetUnknown([]byte{0x80})
	for _, call := range calls {
		t.Run("dispatch"+call.method, func(t *testing.T) {
			if _, err := call.call(ctx, NewV1Client(defaults)); status.Code(err) != codes.Unimplemented {
				t.Fatalf("typed handler returned %v", err)
			}
			if err := defaults.Invoke(ctx, call.method, malformed, empty); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("malformed request returned %v", err)
			}
		})
	}
}
