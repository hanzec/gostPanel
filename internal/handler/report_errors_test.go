package handler

import (
	"context"
	"math"
	"net"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestNodeRegistrationRequiresAnAddressAndPersistsIt(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "test", Port: 39110}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	issued, err := service.NewNodeService(f.db).IssueRegistrationToken(t.Context(), node.ID, "admin", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewReportHandler(f.db)
	req := &nodegrpc.RegisterNodeRequest{Token: "invalid", NodeCertificatePem: testNodeCertificate(t)}
	if _, err := handler.RegisterNode(t.Context(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("register with invalid token: %v", err)
	}
	req.Token = issued.Token
	if _, err := handler.RegisterNode(t.Context(), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("register without an address or peer: %v", err)
	}
	ctx := peer.NewContext(t.Context(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("2001:db8::8"), Port: 39110}})
	if err := f.db.Exec(`CREATE TRIGGER fail_enrollment BEFORE UPDATE OF username ON nodes
		BEGIN SELECT RAISE(ABORT, 'enrollment storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RegisterNode(ctx, req); status.Code(err) != codes.Internal {
		t.Fatalf("register with a storage failure: %v", err)
	}
	if err := f.db.Exec("DROP TRIGGER fail_enrollment").Error; err != nil {
		t.Fatal(err)
	}
	reply, err := handler.RegisterNode(ctx, req)
	if err != nil || reply.GetNodeId() != uint64(node.ID) {
		t.Fatalf("retry register with IPv6 peer: %v, %v", reply, err)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.IPv6 != "2001:db8::8" || node.IPv4 != "" || node.Address != node.IPv6 {
		t.Fatalf("IPv6 peer enrollment: %+v", node)
	}
}

func TestNodeReportRejectsMalformedTrafficAndStorageErrors(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "reporter", Port: 39110, Username: "reporter", Password: "password", TLSCertificatePEM: testNodeCertificate(t)}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.GostRule{Name: "forward", Type: model.RuleTypeForward, NodeID: &node.ID, ListenPort: 9100}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	client := startTestReportServer(t, f.db)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	auth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(node.Username, node.Password))
	report := &nodegrpc.ReportRequest{NodeId: uint64(node.ID)}
	if _, err := client.Report(ctx, report, auth); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("report without observation: %v", err)
	}
	for _, event := range []*observer.Event{
		{Kind: "service", Service: "rule-1", Type: "stats"},
		trafficEvent(rule.ID, math.MaxInt64+1, 0, 0),
		trafficEvent(rule.ID, math.MaxInt64, 1, 0),
		trafficEvent(rule.ID, 0, 0, math.MaxInt64+1),
	} {
		report.Observation = &observer.ObserveRequest{Events: []*observer.Event{event}}
		if _, err := client.Report(ctx, report, auth); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("report with invalid counters %v: %v", event, err)
		}
	}
	// Unmanaged and non-stats events must not prevent processing a valid sample.
	report.Observation = &observer.ObserveRequest{Events: []*observer.Event{
		{Kind: "listener", Service: "rule-1", Type: "stats"},
		{Kind: "service", Service: "unmanaged", Type: "stats"},
		{Kind: "service", Service: "rule-invalid", Type: "stats"},
		{Kind: "service", Service: "rule-0", Type: "stats"},
		{Kind: "service", Service: "rule-1", Type: "status", Status: &observer.ServiceStatus{State: "stopped"}},
		trafficEvent(rule.ID, 3, 4, 1),
	}}
	if reply, err := client.Report(ctx, report, auth); err != nil || !reply.GetOk() {
		t.Fatalf("report with ignored events: %v, %v", reply, err)
	}
	if err := f.db.Exec(`CREATE TRIGGER fail_traffic BEFORE UPDATE OF total_bytes ON rules
		BEGIN SELECT RAISE(ABORT, 'traffic storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	report.Observation.Events = []*observer.Event{trafficEvent(rule.ID, 7, 4, 2)}
	if _, err := client.Report(ctx, report, auth); status.Code(err) != codes.Internal {
		t.Fatalf("report with a storage failure: %v", err)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil || node.TotalBytes != 7 {
		t.Fatalf("failed traffic report changed node counters: %+v, %v", node, err)
	}
}

func TestNodeIPReportHandlesLostUpdatesAndUnavailability(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "reporter", Port: 39110, Username: "reporter", Password: "password", TLSCertificatePEM: testNodeCertificate(t)}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	client := startTestReportServer(t, f.db)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	auth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(node.Username, node.Password))
	req := &nodegrpc.NodeIPsRequest{NodeId: uint64(node.ID)}
	if _, err := client.UpdateNodeIPs(ctx, req, auth); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("IP report without addresses: %v", err)
	}
	req.Ipv6 = "fe80::1%eth0"
	if _, err := client.UpdateNodeIPs(ctx, req, auth); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("IP report with scoped address: %v", err)
	}
	req.Ipv6 = "2001:db8::4"
	if err := f.db.Exec(`CREATE TRIGGER ignore_ip_update BEFORE UPDATE OF ipv6 ON nodes
		BEGIN SELECT RAISE(IGNORE); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateNodeIPs(ctx, req, auth); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("IP report that did not update a row: %v", err)
	}
	if err := f.db.Exec("DROP TRIGGER ignore_ip_update").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TRIGGER fail_ip_update BEFORE UPDATE OF ipv6 ON nodes
		BEGIN SELECT RAISE(ABORT, 'IP storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateNodeIPs(ctx, req, auth); status.Code(err) != codes.Internal {
		t.Fatalf("IP report with a storage failure: %v", err)
	}
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateNodeIPs(ctx, req, auth); status.Code(err) != codes.Internal {
		t.Fatalf("IP report while node storage is unavailable: %v", err)
	}
}
