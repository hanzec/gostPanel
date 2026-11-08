package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const handlerNodePassword = "node-test-password"

func (f *handlerHTTPFixture) enrollFixtureNode(t *testing.T, name string, nodeControl *handlerNodeControl) model.GostNode {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		DNSNames:              []string{nodegrpc.NodeCertificateName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		BasicConstraintsValid: true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		})),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, call grpc.UnaryHandler) (any, error) {
			if !nodegrpc.MatchCredentials(ctx, name, handlerNodePassword) {
				return nil, status.Error(codes.Unauthenticated, "wrong node credentials")
			}
			if err := nodeControl.before(info.FullMethod); err != nil {
				return nil, err
			}
			return call(ctx, req)
		}),
	)
	nodegrpc.RegisterV1Server(server, nodeControl)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	node := model.GostNode{
		Name: name, Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
		Status: model.NodeStatusOnline, Username: name, Password: handlerNodePassword,
		TLSCertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	return node
}

type handlerNodeControl struct {
	nodegrpc.UnimplementedV1Server
	admin       http.Handler
	mu          sync.Mutex
	nft         string
	upgrade     *nodegrpc.NodeUpgradeRequest
	failures    map[string]codes.Code
	restartCall int
}

func (n *handlerNodeControl) reject(method string, code codes.Code) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failures[method] = code
}

func (n *handlerNodeControl) before(method string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if code := n.failures[method]; code != codes.OK {
		return status.Error(code, "fixture node error")
	}
	return nil
}

func (n *handlerNodeControl) Execute(ctx context.Context, req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if n.admin == nil {
		return nil, status.Error(codes.Unimplemented, "fixture has no Gost HTTP API")
	}
	rec := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(ctx, req.Method, req.Path, bytes.NewReader(req.Body))
	n.admin.ServeHTTP(rec, request)
	return &nodegrpc.CommandReply{StatusCode: int32(rec.Code), Body: rec.Body.Bytes()}, nil
}

func (n *handlerNodeControl) GetGostServiceStatus(context.Context, *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	return &nodegrpc.GostServiceStatus{Unit: "gost.service", ActiveState: "active"}, nil
}

func (n *handlerNodeControl) RestartGostService(ctx context.Context, empty *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	n.mu.Lock()
	n.restartCall++
	n.mu.Unlock()
	return n.GetGostServiceStatus(ctx, empty)
}

func (n *handlerNodeControl) GetGostLogs(_ context.Context, req *nodegrpc.GostLogsRequest) (*nodegrpc.GostLogsReply, error) {
	if req.GetLines() != 100 {
		return nil, status.Error(codes.InvalidArgument, "unexpected log line limit")
	}
	return &nodegrpc.GostLogsReply{Entries: []*nodegrpc.GostLogEntry{{Message: "gost started"}}}, nil
}

func (n *handlerNodeControl) GetNftConfig(context.Context, *emptypb.Empty) (*nodegrpc.NftConfig, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &nodegrpc.NftConfig{Config: n.nft}, nil
}

func (n *handlerNodeControl) ApplyNftConfig(_ context.Context, req *nodegrpc.NftConfig) (*nodegrpc.NftConfig, error) {
	if !json.Valid([]byte(req.GetConfig())) {
		return nil, status.Error(codes.InvalidArgument, "invalid nftables JSON")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nft = req.GetConfig()
	return &nodegrpc.NftConfig{Config: n.nft}, nil
}

func (n *handlerNodeControl) GetUpgradeStatus(context.Context, *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	return &nodegrpc.NodeUpgradeStatus{Supported: true, GostVersion: "3.0.0", DaemonVersion: "0.1.1-abcdef0"}, nil
}

func (n *handlerNodeControl) StartUpgrade(_ context.Context, req *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.upgrade = req
	return &nodegrpc.NodeUpgradeStatus{Component: req.GetComponent(), TargetVersion: req.GetVersion(), State: "queued"}, nil
}

type handlerReleaseTransport func(*http.Request) (*http.Response, error)

func (call handlerReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return call(req)
}

func TestNodeHTTPBridgeOperations(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	f.registerSettings()
	control := &handlerNodeControl{
		failures: make(map[string]codes.Code),
		admin: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "GET" || req.URL.Path != "/config" {
				t.Errorf("unexpected Gost admin request: %s %s", req.Method, req.URL)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"services":[{"name":"visible-service","addr":":9000"}]}`)
		}),
	}
	node := f.enrollFixtureNode(t, "bridge", control)
	path := fmt.Sprintf("/nodes/%d", node.ID)

	cfg := handlerData[gost.GostConfig](t, f.request(t, "GET", path+"/config", "", 200))
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "visible-service" {
		t.Fatalf("returned Gost config: %+v", cfg)
	}
	f.request(t, "GET", "/nodes/999/config", "", 500)
	for _, item := range []struct{ method, suffix string }{
		{"GET", "/gost/service"}, {"POST", "/gost/restart"}, {"GET", "/gost/logs"},
		{"GET", "/nft"}, {"PUT", "/nft"},
	} {
		f.request(t, item.method, "/nodes/0"+item.suffix, "", 400)
		f.request(t, item.method, "/nodes/999"+item.suffix, "", 404)
	}
	for _, method := range []string{"GET", "POST"} {
		suffix := "/gost/service"
		if method == "POST" {
			suffix = "/gost/restart"
		}
		state := handlerData[nodegrpc.GostServiceStatus](t, f.request(t, method, path+suffix, "", 200))
		if state.GetUnit() != "gost.service" || state.GetActiveState() != "active" {
			t.Fatalf("%s Gost service status: %+v", method, &state)
		}
	}
	logs := handlerData[nodegrpc.GostLogsReply](t, f.request(t, "GET", path+"/gost/logs", "", 200))
	if entries := logs.GetEntries(); len(entries) != 1 || entries[0].GetMessage() != "gost started" {
		t.Fatalf("bridge log: %+v", &logs)
	}
	for _, test := range []struct {
		rpcCode codes.Code
		http    int
	}{
		{codes.InvalidArgument, 400}, {codes.ResourceExhausted, 400}, {codes.Unimplemented, 501},
		{codes.FailedPrecondition, 409}, {codes.NotFound, 409}, {codes.PermissionDenied, 409},
		{codes.DeadlineExceeded, 504}, {codes.Unavailable, 502}, {codes.Unauthenticated, 502}, {codes.Internal, 500},
	} {
		control.reject(nodegrpc.V1_GetGostServiceStatus_FullMethodName, test.rpcCode)
		f.request(t, "GET", path+"/gost/service", "", test.http)
	}
	control.reject(nodegrpc.V1_RestartGostService_FullMethodName, codes.FailedPrecondition)
	f.request(t, "POST", path+"/gost/restart", "", 409)
	control.reject(nodegrpc.V1_GetGostLogs_FullMethodName, codes.Unimplemented)
	f.request(t, "GET", path+"/gost/logs", "", 501)

	const nftJSON = `{"nftables":[]}`
	f.request(t, "PUT", path+"/nft", "{}", 400)
	f.request(t, "PUT", path+"/nft", `{"config":"not-json"}`, 400)
	f.request(t, "PUT", path+"/nft", `{"config":"`+strings.Repeat("x", nodegrpc.MaxNftConfigBytes+1)+`"}`, 400)
	body, err := json.Marshal(map[string]string{"config": nftJSON})
	if err != nil {
		t.Fatal(err)
	}
	updated := handlerData[nodeNftConfig](t, f.request(t, "PUT", path+"/nft", string(body), 200))
	read := handlerData[nodeNftConfig](t, f.request(t, "GET", path+"/nft", "", 200))
	if updated != read || read.Config != nftJSON || read.Table != nodegrpc.NftTableName(uint64(node.ID)) ||
		!json.Valid([]byte(read.Template)) || !strings.Contains(read.Template, read.Table) {
		t.Fatalf("bridge returned an unexpected nftables config: %+v", read)
	}
	control.reject(nodegrpc.V1_GetNftConfig_FullMethodName, codes.Unimplemented)
	f.request(t, "GET", path+"/nft", "", 501)
	control.mu.Lock()
	restarts := control.restartCall
	control.mu.Unlock()
	if restarts != 1 {
		t.Fatalf("node restarted %d times, want 1", restarts)
	}
	audit := handlerData[handlerPage[model.OperationLog]](t, f.request(t, "GET", "/logs?resource_type=node", "", 200))
	if audit.Total != 2 || len(audit.List) != 2 || audit.List[0].ResourceID != node.ID || audit.List[1].ResourceID != node.ID {
		t.Fatalf("only successful node operations should appear in the audit log: %+v", audit)
	}
}

func TestNodeHTTPUpgradeLifecycle(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	control := &handlerNodeControl{failures: make(map[string]codes.Code)}
	node := f.enrollFixtureNode(t, "upgrade-node", control)
	path := fmt.Sprintf("/nodes/%d/upgrade", node.ID)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = handlerReleaseTransport(func(req *http.Request) (*http.Response, error) {
		var tag string
		switch req.URL.String() {
		case "https://api.github.com/repos/go-gost/gost/releases/latest":
			tag = "v3.1.0"
		case "https://api.github.com/repos/hanzec/gostPanel/releases/latest":
			tag = "0.1.2-abcdef1"
		default:
			return nil, fmt.Errorf("unexpected release lookup: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"tag_name":%q}`, tag)))}, nil
	})
	policy := `{"auto_upgrade_gost":true,"auto_upgrade_daemon":false}`
	f.request(t, "GET", "/nodes/999/upgrade", "", 404)
	f.request(t, "PUT", "/nodes/999/upgrade", policy, 404)
	f.request(t, "GET", "/nodes/0/upgrade", "", 400)
	f.request(t, "PUT", "/nodes/0/upgrade", policy, 400)
	f.request(t, "POST", "/nodes/0/upgrade/gost", "", 400)
	f.request(t, "PUT", path, `{"auto_upgrade_gost":true}`, 400)
	f.request(t, "PUT", path, policy, 200)
	view := handlerData[service.NodeUpgradeView](t, f.request(t, "GET", path, "", 200))
	if !view.AutoUpgradeGost || view.AutoUpgradeDaemon || !view.Available || !view.GostUpdateAvailable || !view.DaemonUpdateAvailable {
		t.Fatalf("upgrade view: %+v", view)
	}
	f.request(t, "POST", path+"/unknown", "", 400)
	state := handlerData[nodegrpc.NodeUpgradeStatus](t, f.request(t, "POST", path+"/gost", "", 200))
	control.mu.Lock()
	started := control.upgrade
	control.mu.Unlock()
	if state.GetState() != "queued" || state.GetTargetVersion() != "v3.1.0" ||
		started.GetVersion() != "v3.1.0" || started.GetComponent() != "gost" {
		t.Fatalf("upgrade request: state %+v, RPC %+v", &state, started)
	}
}
