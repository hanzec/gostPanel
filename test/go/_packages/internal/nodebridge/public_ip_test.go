package nodebridge

import (
	"context"
	"net"
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

func TestCollectNodeIPsSeparatesLocalInterfacesFromPublicIP(t *testing.T) {
	var interfaces []net.Addr
	for _, address := range []string{"198.51.100.1/24", "198.51.100.2/24", "2001:db8::1/64", "10.0.0.1/8", "fc00::1/64", "fe80::1/64", "127.0.0.1/8"} {
		ip, network, err := net.ParseCIDR(address)
		if err != nil {
			t.Fatal(err)
		}
		network.IP = ip
		interfaces = append(interfaces, network)
	}
	const egress = "203.0.113.1"
	report := collectNodeIPs(egress, "", interfaces)
	if report.ipv4 != egress || report.ipv6 != "2001:db8::1" ||
		!slices.Equal(report.public, []string{"198.51.100.1", "198.51.100.2", "2001:db8::1", egress}) ||
		!slices.Equal(report.local, []string{"10.0.0.1", "198.51.100.1", "198.51.100.2", "2001:db8::1", "fc00::1"}) {
		t.Fatalf("IP snapshot = %+v", report)
	}
}

func TestReportPublicIPChanges(t *testing.T) {
	const ipv4, ipv6, newIPv4 = "198.51.100.1", "2001:db8::1", "198.51.100.2"
	snapshots := []nodeIPSnapshot{
		{ipv4: ipv4, ipv6: ipv6}, {ipv4: ipv4, ipv6: ipv6}, {ipv4: ipv4, ipv6: ipv6},
		{}, {ipv4: newIPv4}, {ipv4: newIPv4, ipv6: ipv6},
		{ipv4: ipv4, ipv6: ipv6}, {ipv4: newIPv4, ipv6: ipv6}, {ipv4: newIPv4, ipv6: ipv6},
	}
	want := [][2]string{{ipv4, ipv6}, {ipv4, ipv6}, {newIPv4, ""}, {ipv4, ipv6}, {newIPv4, ipv6}}
	var got [][2]string
	for _, request := range runIPReports(t, snapshots) {
		if request.GetLocalIpSnapshot() != nil {
			t.Fatalf("no local snapshot was detected, but received %+v", request.GetLocalIpSnapshot())
		}
		got = append(got, [2]string{request.GetIpv4(), request.GetIpv6()})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reported addresses = %v, want %v", got, want)
	}
}

func TestReportLocalIPChangesWithoutPublicAddresses(t *testing.T) {
	first, next, empty := []string{"10.0.0.2"}, []string{"fd00::2"}, []string{}
	snapshots := []nodeIPSnapshot{
		{local: first}, {local: first}, {local: first}, {},
		{local: next}, {local: empty}, {local: empty}, {local: empty},
	}
	var got [][]string
	for _, request := range runIPReports(t, snapshots) {
		snapshot := request.GetLocalIpSnapshot()
		if snapshot == nil || request.GetIpv4() != "" || request.GetIpv6() != "" || len(request.GetAddresses()) != 0 {
			t.Fatalf("local-only report = %+v", request)
		}
		got = append(got, snapshot.GetAddresses())
	}
	want := [][]string{first, first, next, empty, empty}
	if !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("local reports = %v, want %v", got, want)
	}
}

func TestReportMultipathTCPChangesAndRetry(t *testing.T) {
	supported, unsupported := true, false
	for _, test := range []struct {
		name   string
		values []*bool
		want   []bool
	}{
		{"first unsupported report", []*bool{&unsupported, &unsupported, &unsupported}, []bool{false, false}},
		{"changes and retries", []*bool{&supported, &supported, &supported, &unsupported, &unsupported, &supported, &supported, &supported}, []bool{true, true, false, true, true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var snapshots []nodeIPSnapshot
			for _, value := range test.values {
				snapshots = append(snapshots, nodeIPSnapshot{local: []string{"10.0.0.2"}, mptcpSupported: value})
			}
			var got []bool
			for _, request := range runIPReports(t, snapshots) {
				if request.MptcpSupported == nil {
					t.Fatal("MPTCP report omitted the detected result")
				}
				if request.GetLocalIpSnapshot() == nil {
					t.Fatal("capability change omitted the available local snapshot needed by older panels")
				}
				got = append(got, request.GetMptcpSupported())
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("MPTCP reports = %v, want %v", got, test.want)
			}
		})
	}
}

func runIPReports(t *testing.T, snapshots []nodeIPSnapshot) []*nodegrpc.NodeIPsRequest {
	t.Helper()
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

	// The last tick waits for the preceding check to finish.
	snapshots = append(snapshots, nodeIPSnapshot{})
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		next := 0
		reportIPChanges(ctx, 42, nodegrpc.NewV1Client(session), func(context.Context) nodeIPSnapshot {
			snapshot := snapshots[next]
			next++
			return snapshot
		}, ticks)
	}()

	for range snapshots[1:] {
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal("IP reporter did not finish its check")
		}
	}
	cancel()
	<-done
	var got []*nodegrpc.NodeIPsRequest
	for len(reports.requests) > 0 {
		request := <-reports.requests
		if request.GetNodeId() != 42 {
			t.Fatalf("reported node ID = %d, want 42", request.GetNodeId())
		}
		got = append(got, request)
	}
	return got
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
