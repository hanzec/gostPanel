package handler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
	"ycgp/internal/model"
	"ycgp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestReportRecordsTrafficForTheEntryNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "traffic.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.GostNode{}, &model.GostTunnel{}, &model.GostRule{}); err != nil {
		t.Fatal(err)
	}
	nodes := []model.GostNode{
		{Name: "entry", Address: "localhost", Port: 39000, Username: "entry", Password: "entry-password"},
		{Name: "exit", Address: "localhost", Port: 39001, Username: "exit", Password: "exit-password"},
	}
	if err := db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	tunnel := model.GostTunnel{Name: "test", EntryNodeID: nodes[0].ID, ExitNodeID: nodes[1].ID}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	rules := []model.GostRule{
		{Name: "through tunnel", Type: model.RuleTypeTunnel, TunnelID: &tunnel.ID, ListenPort: 12001},
		{Name: "on exit", Type: model.RuleTypeForward, NodeID: &nodes[1].ID, ListenPort: 12002},
	}
	if err := db.Create(&rules).Error; err != nil {
		t.Fatal(err)
	}

	client := startTestReportServer(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := &nodegrpc.ReportRequest{
		NodeId: uint64(nodes[0].ID),
		Observation: &observer.ObserveRequest{Events: []*observer.Event{
			trafficEvent(rules[0].ID, 12, 5, 2),
		}},
	}
	if _, err := client.Report(ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("report without credentials = %v", err)
	}
	auth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(nodes[0].Username, nodes[0].Password))
	submit := func(event *observer.Event) {
		t.Helper()
		request.Observation.Events = []*observer.Event{event}
		if reply, err := client.Report(ctx, request, auth); err != nil || !reply.GetOk() {
			t.Fatalf("report = %v, %v", reply, err)
		}
	}
	submit(trafficEvent(rules[0].ID, 12, 5, 2))
	submit(trafficEvent(rules[0].ID, 20, 8, 3))
	submit(trafficEvent(rules[0].ID, 20, 8, 3))
	submit(&observer.Event{
		Kind: "service", Service: fmt.Sprintf("rule-%d", rules[0].ID), Type: "status",
		Status: &observer.ServiceStatus{State: "running"},
	})
	submit(trafficEvent(rules[0].ID, 25, 9, 4))
	submit(trafficEvent(rules[0].ID, 3, 2, 1))
	request.Observation.Events = []*observer.Event{
		trafficEvent(rules[0].ID, 6, 5, 2), trafficEvent(rules[1].ID, 12, 5, 2),
	}
	if _, err := client.Report(ctx, request, auth); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("report for another node's rule = %v", err)
	}

	if err := db.First(&rules[0], rules[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if got := rules[0]; got.InputBytes != 48 || got.OutputBytes != 19 || got.TotalBytes != 67 || got.TotalRequests != 8 {
		t.Fatalf("rule counters = %+v", got)
	}
	if got := rules[0].ObserverCounters; got != (model.ObserverCounters{InputBytes: 3, OutputBytes: 2, Connections: 1}) {
		t.Fatalf("observer checkpoint = %+v", got)
	}
	if err := db.Order("id").Find(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	if nodes[0].TotalBytes != 67 || nodes[1].TotalBytes != 0 {
		t.Fatalf("node counters = %d, %d", nodes[0].TotalBytes, nodes[1].TotalBytes)
	}
}

func trafficEvent(id uint, input, output, connections uint64) *observer.Event {
	return &observer.Event{
		Kind: "service", Service: fmt.Sprintf("rule-%d", id), Type: "stats",
		Stats: &observer.Stats{InputBytes: input, OutputBytes: output, TotalConns: connections},
	}
}

func startTestReportServer(t *testing.T, db *gorm.DB) nodegrpc.PanelClient {
	t.Helper()
	fixture := httptest.NewTLSServer(nil)
	fixture.Close()
	trust := x509.NewCertPool()
	trust.AddCert(fixture.Certificate())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(fixture.TLS)))
	nodegrpc.RegisterPanelServer(server, NewReportHandler(db))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: trust})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return nodegrpc.NewPanelClient(conn)
}
