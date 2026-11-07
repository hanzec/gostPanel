package nodebridge

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ycgp/pkg/gost"
	"ycgp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestNodeBridgeControlAndReport(t *testing.T) {
	const username, password = "node", "test-password"
	var current *gost.ObserverConfig
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			t.Error("bridge did not authenticate to the local Gost API")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/config":
			_, _ = w.Write([]byte(`{"services":[]}`))
		case "GET /api/config/observers/observer-global":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": current})
		case "POST /api/config/observers":
			if current != nil {
				t.Error("existing observer was recreated")
			}
			current = &gost.ObserverConfig{}
			if err := json.NewDecoder(r.Body).Decode(current); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected local request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(admin.Close)
	certFile, keyFile := testTLSFiles(t)
	serverTLS, err := credentials.NewServerTLSFromFile(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &reportRecorder{username: username, password: password, reports: make(chan *nodegrpc.ReportRequest, 1)}
	panel := grpc.NewServer(grpc.Creds(serverTLS))
	nodegrpc.RegisterPanelServer(panel, recorder)
	panelListener := listenLocal(t)
	go func() { _ = panel.Serve(panelListener) }()
	t.Cleanup(panel.Stop)

	controlListener, observerListener := listenLocal(t), listenLocal(t)
	controlAddress, observerAddress := controlListener.Addr().String(), observerListener.Addr().String()
	_ = controlListener.Close()
	_ = observerListener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			NodeID: 42, Username: username, Password: password,
			ControlListen: controlAddress, ObserverListen: observerAddress,
			AdminAddress: strings.TrimPrefix(admin.URL, "http://"), ReportAddress: panelListener.Addr().String(),
			ReportCAFile: certFile, TLSCertFile: certFile, TLSKeyFile: keyFile,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	clientTLS, err := nodegrpc.ClientTLS(certFile)
	if err != nil {
		t.Fatal(err)
	}
	controlConn, err := grpc.NewClient(controlAddress, grpc.WithTransportCredentials(clientTLS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlConn.Close() })
	timeout, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	command := &nodegrpc.CommandRequest{Method: "GET", Path: "/config"}
	if _, err = nodegrpc.NewNodeClient(controlConn).Execute(timeout, command, grpc.WaitForReady(true)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated control request: %v", err)
	}
	client := gost.NewClient(&gost.Config{Address: controlAddress, CAFile: certFile, Username: username, Password: password})
	if err = client.HealthCheck(); err != nil {
		t.Fatal(err)
	}
	address, err := client.ObserverAddress()
	if err != nil || address != observerAddress {
		t.Fatalf("observer address: %q, %v", address, err)
	}
	config := &gost.ObserverConfig{
		Name: "observer-global", Plugin: &gost.PluginConfig{Type: "grpc", Addr: address, Token: client.ObserverToken()},
	}
	if err = client.SetObserver(config); err != nil {
		t.Fatal(err)
	}
	if err = client.SetObserver(config); err != nil {
		t.Fatal(err)
	}

	observerConn, err := grpc.NewClient(observerAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observerConn.Close() })
	observation := &observer.ObserveRequest{Events: []*observer.Event{{
		Kind: "service", Service: "rule-5", Type: "stats", Stats: &observer.Stats{InputBytes: 800, OutputBytes: 300},
	}}}
	clientObserve := observer.NewObserverClient(observerConn)
	if _, err = clientObserve.Observe(timeout, observation); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated local observation: %v", err)
	}
	withToken := metadata.AppendToOutgoingContext(timeout, "token", client.ObserverToken())
	reply, err := clientObserve.Observe(withToken, observation)
	if err != nil || !reply.GetOk() {
		t.Fatalf("observe: %v, %v", reply, err)
	}
	select {
	case report := <-recorder.reports:
		if report.GetNodeId() != 42 || report.GetObservation().GetEvents()[0].GetStats().GetInputBytes() != 800 {
			t.Fatalf("unexpected report: %v", report)
		}
	default:
		t.Fatal("no report reached the panel")
	}
}

type reportRecorder struct {
	nodegrpc.UnimplementedPanelServer
	username, password string
	reports            chan *nodegrpc.ReportRequest
}

func (s *reportRecorder) Report(ctx context.Context, report *nodegrpc.ReportRequest) (*observer.ObserveReply, error) {
	if !nodegrpc.MatchCredentials(ctx, s.username, s.password) {
		return nil, status.Error(codes.Unauthenticated, "invalid report credentials")
	}
	s.reports <- report
	return &observer.ObserveReply{Ok: true}, nil
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

func testTLSFiles(t *testing.T) (string, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	key, err := x509.MarshalPKCS8PrivateKey(server.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
