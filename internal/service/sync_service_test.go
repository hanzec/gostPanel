package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestRuleSyncRetargetsRunningTunnel(t *testing.T) {
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
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

	const original = `{"name":"tunnel-1-chain","metadata":{"retries":2},"hops":[{"name":"hop-0","selector":{"strategy":"fifo"},"nodes":[{"name":"exit-relay","addr":"[2001:db8::8]:8443","connector":{"type":"relay","metadata":{"nodelay":true}},"dialer":{"type":"tls","tls":{"serverName":"relay.example"}}}]}]}`
	updated := strings.Replace(original, "[2001:db8::8]:8443", "[2001:db8::9]:8443", 1)
	api := &tunnelSyncNode{t: t, chain: json.RawMessage(original)}
	entry := startTunnelSyncNode(t, api)
	exit := model.GostNode{Name: "exit", Address: "2001:db8::8", Port: 39000, IPv6: "2001:db8::8"}
	for _, node := range []*model.GostNode{&entry, &exit} {
		if err := db.Create(node).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The panel may mark the tunnel stopped when the exit goes offline, even though its chain still exists.
	tunnels := []model.GostTunnel{
		{ID: 1, Name: "live chain", EntryNodeID: entry.ID, ExitNodeID: exit.ID, RelayPort: 8449, ChainID: "tunnel-1-chain", Status: model.TunnelStatusStopped},
		{ID: 2, Name: "absent chain", EntryNodeID: entry.ID, ExitNodeID: exit.ID, RelayPort: 8444, ChainID: "tunnel-2-chain", Status: model.TunnelStatusStopped},
	}
	if err := db.Create(&tunnels).Error; err != nil {
		t.Fatal(err)
	}
	nodes := repository.NewNodeRepository(db)
	if err := nodes.UpdateIPs(context.Background(), &exit, "", "2001:db8::9"); err != nil {
		t.Fatal(err)
	}
	updatedExit, err := nodes.FindByID(exit.ID)
	if err != nil {
		t.Fatal(err)
	}
	exitAddresses := map[uint]string{updatedExit.ID: updatedExit.Address}
	service := NewRuleSyncService(db)
	assertChain := func(want string, updates, saves int) {
		t.Helper()
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.updates != updates || api.saves != saves {
			t.Errorf("chain PUTs and config saves = %d, %d; want %d, %d", api.updates, api.saves, updates, saves)
		}
		var gotConfig, wantConfig any
		if err := json.Unmarshal(api.chain, &gotConfig); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantConfig); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotConfig, wantConfig) {
			t.Errorf("chain = %s; want %s", api.chain, want)
		}
	}
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(original, 1, 0)
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(updated, 2, 1)
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(updated, 2, 2)
	service.syncNodeRules(entry, model.GostAllowlist{}, exitAddresses)
	assertChain(updated, 2, 2)
	for i, want := range []model.TunnelStatus{model.TunnelStatusRunning, model.TunnelStatusStopped} {
		var stored model.GostTunnel
		if err := db.First(&stored, tunnels[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != want {
			t.Errorf("tunnel %d status = %q; want %q", stored.ID, stored.Status, want)
		}
	}
}

type tunnelSyncNode struct {
	nodegrpc.UnimplementedNodeServer
	t       *testing.T
	mu      sync.Mutex
	chain   json.RawMessage
	updates int
	saves   int
}

func (s *tunnelSyncNode) Execute(ctx context.Context, request *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if !nodegrpc.MatchCredentials(ctx, "entry", "fixture-password") {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	response := &nodegrpc.CommandReply{StatusCode: http.StatusOK}
	switch request.GetMethod() + " " + request.GetPath() {
	case "GET /config":
		response.Body = fmt.Appendf(nil, `{"chains":[%s]}`, s.chain)
	case "GET /config/chains/tunnel-1-chain":
		response.Body = fmt.Appendf(nil, `{"data":%s}`, s.chain)
	case "PUT /config/chains/tunnel-1-chain":
		s.updates++
		if s.updates == 1 {
			response.StatusCode = http.StatusServiceUnavailable
			break
		}
		s.chain = request.GetBody()
	case "POST /config?format=yaml":
		s.saves++
		if s.saves == 1 {
			response.StatusCode = http.StatusServiceUnavailable
		}
	default:
		s.t.Errorf("unexpected Gost command %s %s", request.GetMethod(), request.GetPath())
		return nil, status.Errorf(codes.InvalidArgument, "unexpected Gost command %s %s", request.GetMethod(), request.GetPath())
	}
	return response, nil
}

func startTunnelSyncNode(t *testing.T, server nodegrpc.NodeServer) model.GostNode {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		DNSNames: []string{nodegrpc.NodeCertificateName},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: private}},
	})))
	nodegrpc.RegisterNodeServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	return model.GostNode{
		Name: "entry", Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Status: model.NodeStatusOnline,
		Username: "entry", Password: "fixture-password", TLSCertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}
