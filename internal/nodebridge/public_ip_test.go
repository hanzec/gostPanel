package nodebridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestReportPublicIPChanges(t *testing.T) {
	reports := &ipReportRecorder{requests: make(chan *nodegrpc.NodeIPsRequest, 8)}
	panel := httptest.NewTLSServer(reports)
	t.Cleanup(panel.Close)
	client := panel.Client()
	client.Transport.(*http.Transport).DialContext = dialPanelAt(panel.Listener)
	host, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := host.DialSession(ctx, client, 42, testAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
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

func (s *ipReportRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	stream, err := nodegrpc.AcceptSession(w, r)
	if err != nil {
		return
	}
	defer stream.Close()
	_ = nodegrpc.NewSession(stream, nodegrpc.NewDispatcher(s, nodegrpc.V1_UpdateNodeIPs_FullMethodName)).Run()
}

func (s *ipReportRecorder) UpdateNodeIPs(_ context.Context, req *nodegrpc.NodeIPsRequest) (*emptypb.Empty, error) {
	s.requests <- req
	if call := s.calls.Add(1); call == 1 || call == 4 {
		return nil, status.Error(codes.Unavailable, "temporary panel failure")
	}
	return &emptypb.Empty{}, nil
}
