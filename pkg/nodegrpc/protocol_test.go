package nodegrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type protocolExchange struct {
	method   string
	request  proto.Message
	response proto.Message
	call     func(context.Context, *grpc.ClientConn, ...grpc.CallOption) (proto.Message, error)
}

func exchange[Client any, Request, Response proto.Message](
	newClient func(grpc.ClientConnInterface) Client,
	method string,
	call func(Client, context.Context, Request, ...grpc.CallOption) (Response, error),
	request Request,
	response Response,
) protocolExchange {
	return protocolExchange{method, request, response, func(ctx context.Context, conn *grpc.ClientConn, opts ...grpc.CallOption) (proto.Message, error) {
		return call(newClient(conn), ctx, request, opts...)
	}}
}

func TestNodeAndPanelWireCompatibility(t *testing.T) {
	const username, password = "node-42", "fixture-password"
	empty := &emptypb.Empty{}
	gostStatus := &GostServiceStatus{Unit: "yagp-gost-42.service", LoadState: "loaded", ActiveState: "active", SubState: "running"}
	config := &NftConfig{Config: "{\"nftables\":[]}"}
	// Carry a newer peer's field through the older protobuf schema in both directions.
	config.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 100, protowire.VarintType), 43))
	forwardedReport := &ReportRequest{
		NodeId: 42,
		Observation: &observer.ObserveRequest{Events: []*observer.Event{{
			Kind: "service", Service: "rule-1", Type: "stats", Stats: &observer.Stats{InputBytes: 10, OutputBytes: 20},
		}}},
	}
	calls := []protocolExchange{
		exchange(NewNodeClient, Node_Execute_FullMethodName, NodeClient.Execute,
			&CommandRequest{Method: "POST", Path: "/config/services", Body: []byte("{\"name\":\"test\"}")},
			&CommandReply{StatusCode: 201, Body: []byte("{\"created\":true}")}),
		exchange(NewNodeClient, Node_GetObserverEndpoint_FullMethodName, NodeClient.GetObserverEndpoint,
			empty, &ObserverEndpoint{Address: "127.0.0.1:10900"}),
		exchange(NewNodeClient, Node_GetGostServiceStatus_FullMethodName, NodeClient.GetGostServiceStatus,
			empty, gostStatus),
		exchange(NewNodeClient, Node_RestartGostService_FullMethodName, NodeClient.RestartGostService,
			empty, gostStatus),
		exchange(NewNodeClient, Node_GetGostLogs_FullMethodName, NodeClient.GetGostLogs,
			&GostLogsRequest{Lines: 2}, &GostLogsReply{Entries: []*GostLogEntry{
				{TimestampUnixMicros: 1730000000000000, Message: "started", Priority: 6},
				{TimestampUnixMicros: 1730000000000001, Message: "connection refused", Priority: 3},
			}}),
		exchange(NewNodeClient, Node_GetNftConfig_FullMethodName, NodeClient.GetNftConfig,
			empty, config),
		exchange(NewNodeClient, Node_ApplyNftConfig_FullMethodName, NodeClient.ApplyNftConfig,
			config, config),
		exchange(NewNodeClient, Node_SyncTrafficQuota_FullMethodName, NodeClient.SyncTrafficQuota,
			&TrafficQuotaRequest{LimitBytes: 1000, UsedBytes: 123, WindowStart: 456, TcpPorts: []uint32{80, 443}, UdpPorts: []uint32{53}},
			&TrafficQuotaReply{UsedBytes: 200}),
		exchange(NewNodeClient, Node_GetUpgradeStatus_FullMethodName, NodeClient.GetUpgradeStatus, empty,
			&NodeUpgradeStatus{Supported: true, GostVersion: "v3.3.2", DaemonVersion: "0.1.99-abc1234", Component: "gost",
				TargetVersion: "v3.3.3", State: "failed", Error: "restart failed"}),
		exchange(NewNodeClient, Node_StartUpgrade_FullMethodName, NodeClient.StartUpgrade,
			&NodeUpgradeRequest{Component: "gost", Version: "v3.3.3"},
			&NodeUpgradeStatus{Supported: true, Component: "gost", TargetVersion: "v3.3.3", State: "queued"}),
		exchange(NewPanelClient, Panel_Report_FullMethodName, PanelClient.Report, forwardedReport, &observer.ObserveReply{Ok: true}),
		exchange(NewPanelClient, Panel_RegisterNode_FullMethodName, PanelClient.RegisterNode,
			&RegisterNodeRequest{Token: "42.fixture", NodeCertificatePem: "fixture cert", Ipv4: "203.0.113.42", Ipv6: "2001:db8::42"},
			&RegisterNodeReply{NodeId: 42, Username: username, Password: password, ControlPort: 8443}),
		exchange(NewPanelClient, Panel_GetNftTemplate_FullMethodName, PanelClient.GetNftTemplate,
			&NftTemplateRequest{NodeId: 42}, config),
		exchange(NewPanelClient, Panel_UpdateNodeIPs_FullMethodName, PanelClient.UpdateNodeIPs,
			&NodeIPsRequest{NodeId: 42, Ipv4: "203.0.113.42", Ipv6: "2001:db8::42"}, empty),
	}
	byMethod := make(map[string]protocolExchange, len(calls))
	for _, call := range calls {
		byMethod[call.method] = call
	}
	cert, pem := testNodeCertificate(t, nil, false)
	control, err := NodeClientTLS(pem)
	if err != nil {
		t.Fatal(err)
	}
	auth := grpc.WithPerRPCCredentials(PasswordCredentials(username, password))
	active := protocolListener(t, cert, grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !MatchCredentials(ctx, username, password) {
			return nil, status.Error(codes.Unauthenticated, "missing node credentials")
		}
		call, ok := byMethod[info.FullMethod]
		message, messageOK := request.(proto.Message)
		if !ok || !messageOK || !proto.Equal(message, call.request) {
			return nil, status.Errorf(codes.InvalidArgument, "unexpected wire request for %s", info.FullMethod)
		}
		if len(metadata.ValueFromIncomingContext(ctx, "test-unimplemented")) != 0 {
			return handler(ctx, request)
		}
		return call.response, nil
	}))
	implemented := protocolConn(t, active, control, auth)
	defaults := protocolConn(t, protocolListener(t, cert), control, auth)
	for _, call := range calls {
		t.Run(call.method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reply, err := call.call(ctx, implemented)
			if err != nil || !proto.Equal(reply, call.response) {
				t.Fatalf("wire reply = %v, %v, want %v", reply, err, call.response)
			}
			if _, err := call.call(ctx, defaults); status.Code(err) != codes.Unimplemented {
				t.Fatalf("unimplemented endpoint returned %v", err)
			}
			viaInterceptor := metadata.AppendToOutgoingContext(ctx, "test-unimplemented", "true")
			methodName := call.method[strings.LastIndexByte(call.method, '/')+1:]
			if _, err := call.call(viaInterceptor, implemented); status.Code(err) != codes.Unimplemented ||
				!strings.Contains(status.Convert(err).Message(), methodName) {
				t.Fatalf("interceptor delegated to unexpected RPC: %v", err)
			}
			if _, err := call.call(ctx, implemented, grpc.ForceCodec(malformedProtocolCodec{})); status.Code(err) != codes.Internal {
				t.Fatalf("malformed protobuf returned %v", err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unauthenticated := protocolConn(t, active, control)
	if _, err := NewNodeClient(unauthenticated).GetObserverEndpoint(ctx, empty); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated endpoint returned %v", err)
	}
	_, foreignPEM := testNodeCertificate(t, nil, false)
	foreignRoot, err := NodeClientTLS(foreignPEM)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := protocolConn(t, active, foreignRoot, auth)
	if _, err := NewNodeClient(untrusted).GetObserverEndpoint(ctx, empty); status.Code(err) != codes.Unavailable {
		t.Fatalf("certificate pin mismatch returned %v", err)
	}
}

func protocolListener(t *testing.T, cert tls.Certificate, options ...grpc.ServerOption) *bufconn.Listener {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	})))...)
	RegisterNodeServer(server, UnimplementedNodeServer{})
	RegisterPanelServer(server, UnimplementedPanelServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener
}

func protocolConn(t *testing.T, listener *bufconn.Listener, creds credentials.TransportCredentials, options ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///node", append(options,
		grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type malformedProtocolCodec struct{}

func (malformedProtocolCodec) Name() string                { return "proto" }
func (malformedProtocolCodec) Marshal(any) ([]byte, error) { return []byte{0x80}, nil }
func (malformedProtocolCodec) Unmarshal([]byte, any) error {
	return errors.New("unexpected response body")
}
