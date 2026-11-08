package nodebridge

import (
	"context"
	"crypto/x509"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestReportPublicIPChanges(t *testing.T) {
	cert, key, _ := testTLSFiles(t, x509.Certificate{IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}})
	serverTLS, err := credentials.NewServerTLSFromFile(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	listener := listenLocal(t)
	panel := grpc.NewServer(grpc.Creds(serverTLS))
	reports := &ipReportRecorder{requests: make(chan *nodegrpc.NodeIPsRequest, 8)}
	nodegrpc.RegisterPanelServer(panel, reports)
	go func() { _ = panel.Serve(listener) }()
	t.Cleanup(panel.Stop)
	conn, err := connectPanel(Config{
		Username: "node", Password: "test-password", Host: "127.0.0.1", ReportCAFile: cert,
	}, dialPanelAt(listener))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := nodegrpc.NewV1Client(conn).Connect(metadata.AppendToOutgoingContext(ctx, nodegrpc.NodeIDMetadata, "42"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Header(); err != nil {
		t.Fatal(err)
	}
	session := nodegrpc.NewSession(stream, nodegrpc.NewDispatcher(nodegrpc.UnimplementedV1Server{}))
	go func() { _ = session.Run() }()

	const ipv4, ipv6, newIPv4 = "198.51.100.1", "2001:db8::1", "198.51.100.2"
	addresses := [][2]string{
		{ipv4, ipv6}, {ipv4, ipv6}, {ipv4, ipv6},
		{}, {newIPv4, ""}, {newIPv4, ipv6},
		{ipv4, ipv6}, {newIPv4, ipv6}, {newIPv4, ipv6},
		{}, // The last tick waits for the preceding check to finish.
	}
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		next := 0
		reportPublicIPChanges(ctx, 42, nodegrpc.NewV1Client(session), func(context.Context) (string, string) {
			address := addresses[next]
			next++
			return address[0], address[1]
		}, ticks)
	}()

	for range addresses[1:] {
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal("IP reporter did not finish its check")
		}
	}
	cancel()
	<-done
	want := [][2]string{{ipv4, ipv6}, {ipv4, ipv6}, {newIPv4, ""}, {ipv4, ipv6}, {newIPv4, ipv6}}
	var got [][2]string
	for len(reports.requests) > 0 {
		request := <-reports.requests
		if request.GetNodeId() != 42 {
			t.Fatalf("reported node ID = %d, want 42", request.GetNodeId())
		}
		got = append(got, [2]string{request.GetIpv4(), request.GetIpv6()})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reported addresses = %v, want %v", got, want)
	}
}

type ipReportRecorder struct {
	nodegrpc.UnimplementedV1Server
	requests chan *nodegrpc.NodeIPsRequest
	calls    atomic.Int32
}

func (s *ipReportRecorder) Connect(stream nodegrpc.V1_ConnectServer) error {
	if !nodegrpc.MatchCredentials(stream.Context(), "node", "test-password") {
		return status.Error(codes.Unauthenticated, "invalid report credentials")
	}
	if err := stream.SendHeader(metadata.Pairs(nodegrpc.ReadyMetadata, "1")); err != nil {
		return err
	}
	return nodegrpc.NewSession(stream, nodegrpc.NewDispatcher(s, nodegrpc.V1_UpdateNodeIPs_FullMethodName)).Run()
}

func (s *ipReportRecorder) UpdateNodeIPs(_ context.Context, req *nodegrpc.NodeIPsRequest) (*emptypb.Empty, error) {
	s.requests <- req
	if call := s.calls.Add(1); call == 1 || call == 4 {
		return nil, status.Error(codes.Unavailable, "temporary panel failure")
	}
	return &emptypb.Empty{}, nil
}
