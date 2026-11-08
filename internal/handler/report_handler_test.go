package handler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
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
		{Name: "entry", Address: "localhost", Username: "entry", Password: "entry-password"},
		{Name: "exit", Address: "localhost", Username: "exit", Password: "exit-password"},
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
	if _, err := client.Report(ctx, request); status.Code(err) != codes.Unimplemented {
		t.Fatalf("report outside the stream = %v", err)
	}
	reports := startTestReportStream(t, ctx, client, nodes[0], nil)
	submit := func(event *observer.Event) {
		t.Helper()
		request.Observation.Events = []*observer.Event{event}
		if reply, err := reports.Report(ctx, request); err != nil || !reply.GetOk() {
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
	if _, err := reports.Report(ctx, request); status.Code(err) != codes.PermissionDenied {
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

func TestReportStreamAuthenticatesOneNodeIdentity(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "registered", Username: "node", Password: "secret"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	client := startTestReportServer(t, f.db)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	id := uint64(node.ID)
	validID := strconv.FormatUint(id, 10)
	auth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(node.Username, node.Password))
	for _, ids := range [][]string{nil, {"bad-id"}, {"0"}, {validID, validID}} {
		md := metadata.MD{nodegrpc.NodeIDMetadata: ids}
		stream, err := client.Connect(metadata.NewOutgoingContext(ctx, md), auth)
		if err == nil {
			_, err = stream.Recv()
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("Connect with node IDs %v = %v", ids, err)
		}
	}
	reports := startTestReportStream(t, ctx, client, node, nil)
	if _, err := reports.Report(ctx, &nodegrpc.ReportRequest{NodeId: id + 1}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("traffic report for another node = %v", err)
	}
	if _, err := reports.GetNftTemplate(ctx, &nodegrpc.NftTemplateRequest{NodeId: id + 1}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nft template request for another node = %v", err)
	}
	if _, err := reports.GetNftTemplate(ctx, &nodegrpc.NftTemplateRequest{NodeId: id}); err != nil {
		t.Fatalf("valid request after rejected requests = %v", err)
	}
}

func TestReportStreamAuthenticationTimeoutKeepsEstablishedStream(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "registered", Username: "node", Password: "secret"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	client := startTestReportServer(t, f.db)
	ctx, cancel := context.WithTimeout(t.Context(), connectAuthTimeout+5*time.Second)
	defer cancel()
	reports := startTestReportStream(t, ctx, client, node, nil)

	// Occupy the database's only connection to stall authentication at the SQL pool.
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	auth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(node.Username, node.Password))
	_, authErr := connectTestReportStream(ctx, client, uint64(node.ID), auth)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if status.Code(authErr) != codes.DeadlineExceeded || ctx.Err() != nil {
		t.Fatalf("Connect stalled by the SQL pool = %v, client context = %v", authErr, ctx.Err())
	}
	if _, err := reports.GetNftTemplate(ctx, &nodegrpc.NftTemplateRequest{NodeId: uint64(node.ID)}); err != nil {
		t.Fatalf("established stream after authentication timeout = %v", err)
	}
}

func trafficEvent(id uint, input, output, connections uint64) *observer.Event {
	return &observer.Event{
		Kind: "service", Service: fmt.Sprintf("rule-%d", id), Type: "stats",
		Stats: &observer.Stats{InputBytes: input, OutputBytes: output, TotalConns: connections},
	}
}

func startTestReportServer(t *testing.T, db *gorm.DB) nodegrpc.V1Client {
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
	nodegrpc.RegisterV1Server(server, NewReportHandler(db, false))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: trust})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return nodegrpc.NewV1Client(conn)
}

func connectTestReportStream(ctx context.Context, client nodegrpc.V1Client, id uint64, options ...grpc.CallOption) (nodegrpc.V1_ConnectClient, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, nodegrpc.NodeIDMetadata, strconv.FormatUint(id, 10))
	stream, err := client.Connect(ctx, options...)
	if err != nil {
		return nil, err
	}
	header, err := stream.Header()
	if err != nil {
		return nil, err
	}
	if len(header.Get(nodegrpc.ReadyMetadata)) != 1 {
		_, err = stream.Recv()
		return nil, err
	}
	return stream, nil
}

func startTestReportStream(t *testing.T, ctx context.Context, client nodegrpc.V1Client, node model.GostNode, commands nodegrpc.StreamHandler) nodegrpc.V1Client {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	auth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(node.Username, node.Password))
	stream, err := connectTestReportStream(ctx, client, uint64(node.ID), auth)
	if err != nil {
		t.Fatal(err)
	}
	session := nodegrpc.NewSession(stream, commands)
	t.Cleanup(session.Close)
	go func() { _ = session.Run() }()
	return nodegrpc.NewV1Client(session)
}
