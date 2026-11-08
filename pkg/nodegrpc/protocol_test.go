package nodegrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http/httptest"
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

func exchange[Request, Response proto.Message](
	method string,
	call func(V1Client, context.Context, Request, ...grpc.CallOption) (Response, error),
	request Request,
	response Response,
) protocolExchange {
	return protocolExchange{method, request, response, func(ctx context.Context, conn *grpc.ClientConn, opts ...grpc.CallOption) (proto.Message, error) {
		return call(NewV1Client(conn), ctx, request, opts...)
	}}
}

func TestV1WireRoundTrip(t *testing.T) {
	const username, password = "node-42", "fixture-password"
	if V1_Execute_FullMethodName != "/yagp.v1/Execute" {
		t.Fatalf("node control path = %s", V1_Execute_FullMethodName)
	}
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
			empty, config),
		exchange(V1_ApplyNftConfig_FullMethodName, V1Client.ApplyNftConfig,
			config, config),
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
		exchange(V1_RegisterNode_FullMethodName, V1Client.RegisterNode,
			&RegisterNodeRequest{Token: "42.fixture", Ipv4: "203.0.113.42", Ipv6: "2001:db8::42"},
			&RegisterNodeReply{NodeId: 42, Username: username, Password: password}),
		exchange(V1_GetNftTemplate_FullMethodName, V1Client.GetNftTemplate,
			&NftTemplateRequest{NodeId: 42}, config),
		exchange(V1_UpdateNodeIPs_FullMethodName, V1Client.UpdateNodeIPs,
			&NodeIPsRequest{NodeId: 42, Ipv4: "203.0.113.42", Ipv6: "2001:db8::42"}, empty),
	}
	byMethod := make(map[string]protocolExchange, len(calls))
	for _, call := range calls {
		byMethod[call.method] = call
	}
	https := httptest.NewTLSServer(nil)
	cert := https.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(https.Certificate())
	https.Close()
	control := credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "127.0.0.1"})
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
	if _, err := NewV1Client(unauthenticated).GetObserverEndpoint(ctx, empty); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated endpoint returned %v", err)
	}
	foreignRoot := credentials.NewTLS(&tls.Config{RootCAs: x509.NewCertPool(), ServerName: "127.0.0.1"})
	untrusted := protocolConn(t, active, foreignRoot, auth)
	if _, err := NewV1Client(untrusted).GetObserverEndpoint(ctx, empty); status.Code(err) != codes.Unavailable {
		t.Fatalf("untrusted certificate returned %v", err)
	}
}

func protocolListener(t *testing.T, cert tls.Certificate, options ...grpc.ServerOption) *bufconn.Listener {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	})))...)
	RegisterV1Server(server, UnimplementedV1Server{})
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
