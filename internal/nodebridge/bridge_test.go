package nodebridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yagp/internal/buildinfo"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestTunnelChainUpdatesAllowed(t *testing.T) {
	for name, want := range map[string]bool{
		"tunnel-42-chain":   true,
		"custom-chain":      false,
		"tunnel-0-chain":    false,
		"tunnel-042-chain":  false,
		"tunnel-42-chain-1": false,
	} {
		if got := allowedCommand(http.MethodPut, "/config/chains/"+name); got != want {
			t.Errorf("PUT chain %s allowed = %t, want %t", name, got, want)
		}
	}
}

func TestNodeBridgeControlAndReport(t *testing.T) {
	const username, password = "node", "test-password"
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			t.Error("bridge did not authenticate to the local Gost API")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != "GET" || r.URL.Path != "/api/config" {
			t.Errorf("unexpected local request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"services":[]}`))
	}))
	t.Cleanup(admin.Close)
	recorder := &reportRecorder{
		username: username, password: password, reports: make(chan *nodegrpc.ReportRequest, 2),
		sessions: make(chan *nodegrpc.Session, 2),
	}
	panel := grpc.NewServer()
	nodegrpc.RegisterV1Server(panel, recorder)
	t.Cleanup(panel.Stop)
	var attempts atomic.Int32
	limitedAt := make(chan time.Time, 1)
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 || r.URL.Path != nodegrpc.PanelProxyPath+"/Connect" {
			t.Errorf("unexpected panel request: %s %s", r.Proto, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch attempts.Add(1) {
		case 2:
			limitedAt <- time.Now()
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
			return
		case 3:
			if delay := time.Since(<-limitedAt); delay < 2*time.Second {
				t.Errorf("reconnect after HTTP 429 waited %v, want at least 2 seconds", delay)
			}
		}
		r.URL.Path = nodegrpc.V1_Connect_FullMethodName
		panel.ServeHTTP(w, r)
	}))
	proxy.EnableHTTP2 = true
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	panelCertFile := filepath.Join(t.TempDir(), "panel-ca.pem")
	if err := os.WriteFile(panelCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}

	observerListener := listenLocal(t)
	observerAddress := observerListener.Addr().String()
	_ = observerListener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			NodeID: 42, Username: username, Password: password, ObserverListen: observerAddress,
			AdminAddress: strings.TrimPrefix(admin.URL, "http://"), ReportAddress: proxy.Listener.Addr().String() + nodegrpc.PanelProxyPath,
			ReportCAFile: panelCertFile,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	observerConn, err := grpc.NewClient(observerAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observerConn.Close() })
	timeout, stop := context.WithTimeout(t.Context(), 12*time.Second)
	defer stop()
	if ready, err := healthpb.NewHealthClient(observerConn).Check(timeout,
		&healthpb.HealthCheckRequest{Service: daemonHealthService + buildinfo.Version}, grpc.WaitForReady(true)); err != nil ||
		ready.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("observer readiness = %v, %v", ready, err)
	}

	command := &nodegrpc.CommandRequest{Method: "GET", Path: "/config"}
	if _, err = nodegrpc.NewV1Client(observerConn).Execute(timeout, command); status.Code(err) != codes.Unimplemented {
		t.Fatalf("local observer exposed management RPC: %v", err)
	}
	observation := &observer.ObserveRequest{Events: []*observer.Event{{
		Kind: "service", Service: "rule-5", Type: "stats", Stats: &observer.Stats{InputBytes: 800, OutputBytes: 300},
	}}}
	localObserver := observer.NewObserverClient(observerConn)
	if _, err = localObserver.Observe(timeout, observation); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated local observation: %v", err)
	}
	withToken := metadata.AppendToOutgoingContext(timeout, "token", nodegrpc.ObserverToken(username, password))

	for attempt := 1; attempt <= 2; attempt++ {
		var session *nodegrpc.Session
		select {
		case session = <-recorder.sessions:
		case <-timeout.Done():
			t.Fatalf("connection %d did not reach panel: %v", attempt, timeout.Err())
		}
		panelClient := nodegrpc.NewV1Client(session)
		reply, err := panelClient.Execute(timeout, command)
		if err != nil || reply.GetStatusCode() != http.StatusOK || string(reply.GetBody()) != `{"services":[]}` {
			t.Fatalf("command on connection %d: %v, %v", attempt, reply, err)
		}
		if endpoint, err := panelClient.GetObserverEndpoint(timeout, &emptypb.Empty{}); err != nil || endpoint.GetAddress() != observerAddress {
			t.Fatalf("observer address: %v, %v", endpoint, err)
		}
		if _, err := panelClient.RegisterNode(timeout, &nodegrpc.RegisterNodeRequest{}); status.Code(err) != codes.Unimplemented {
			t.Fatalf("panel registration RPC in reverse direction: %v", err)
		}
		result, err := localObserver.Observe(withToken, observation)
		if err != nil || !result.GetOk() {
			t.Fatalf("observe on connection %d: %v, %v", attempt, result, err)
		}
		select {
		case report := <-recorder.reports:
			if report.GetNodeId() != 42 || report.GetObservation().GetEvents()[0].GetStats().GetInputBytes() != 800 {
				t.Fatalf("unexpected report: %v", report)
			}
		default:
			t.Fatal("no report reached the panel")
		}
		session.Close()
	}
}

type reportRecorder struct {
	nodegrpc.UnimplementedV1Server
	username, password string
	reports            chan *nodegrpc.ReportRequest
	sessions           chan *nodegrpc.Session
}

func (s *reportRecorder) Connect(stream nodegrpc.V1_ConnectServer) error {
	if !nodegrpc.MatchCredentials(stream.Context(), s.username, s.password) {
		return status.Error(codes.Unauthenticated, "invalid node credentials")
	}
	ids := metadata.ValueFromIncomingContext(stream.Context(), nodegrpc.NodeIDMetadata)
	if len(ids) != 1 || ids[0] != "42" {
		return status.Error(codes.Unauthenticated, "invalid node ID")
	}
	if err := stream.SendHeader(metadata.Pairs(nodegrpc.ReadyMetadata, "1")); err != nil {
		return err
	}
	session := nodegrpc.NewSession(stream, nodegrpc.NewDispatcher(s,
		nodegrpc.V1_Report_FullMethodName, nodegrpc.V1_UpdateNodeIPs_FullMethodName))
	s.sessions <- session
	return session.Run()
}

func (s *reportRecorder) Report(_ context.Context, report *nodegrpc.ReportRequest) (*observer.ObserveReply, error) {
	s.reports <- report
	return &observer.ObserveReply{Ok: true}, nil
}

func (*reportRecorder) UpdateNodeIPs(context.Context, *nodegrpc.NodeIPsRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func testTLSFiles(t *testing.T, certificate x509.Certificate) (string, string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate.SerialNumber = big.NewInt(1)
	certificate.NotBefore = time.Now().Add(-time.Minute)
	certificate.NotAfter = time.Now().Add(time.Hour)
	certificate.BasicConstraintsValid = true
	certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	der, err := x509.CreateCertificate(rand.Reader, &certificate, &certificate, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = os.WriteFile(certFile, certificatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, string(certificatePEM)
}
