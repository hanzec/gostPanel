package nodebridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
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
	panelCertFile, panelKeyFile, _ := testTLSFiles(t, x509.Certificate{IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}})
	nodeCertFile, nodeKeyFile, nodeCert := testTLSFiles(t, x509.Certificate{DNSNames: []string{nodegrpc.NodeCertificateName}})
	_, _, differentNodeCert := testTLSFiles(t, x509.Certificate{DNSNames: []string{nodegrpc.NodeCertificateName}})
	serverTLS, err := credentials.NewServerTLSFromFile(panelCertFile, panelKeyFile)
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
			ReportCAFile: panelCertFile, TLSCertFile: nodeCertFile, TLSKeyFile: nodeKeyFile,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	clientTLS, err := nodegrpc.NodeClientTLS(nodeCert)
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
	controlClient := nodegrpc.NewNodeClient(controlConn)
	if _, err = controlClient.Execute(timeout, command, grpc.WaitForReady(true)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated control request: %v", err)
	}
	if _, err = controlClient.RestartGostService(timeout, &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated restart request: %v", err)
	}
	if _, err = controlClient.StartUpgrade(timeout, &nodegrpc.NodeUpgradeRequest{Component: "gost", Version: "v3.3.3"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated upgrade request: %v", err)
	}
	if _, err = controlClient.ApplyNftConfig(timeout, &nodegrpc.NftConfig{Config: `{"nftables":[]}`}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated nftables request: %v", err)
	}
	configWithoutCertificate := &gost.Config{Address: controlAddress, Username: username, Password: password}
	if err = gost.NewClient(configWithoutCertificate).HealthCheck(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("node without a registration certificate: %v", err)
	}
	wrongCertificate := *configWithoutCertificate
	wrongCertificate.CertificatePEM = differentNodeCert
	if err = gost.NewClient(&wrongCertificate).HealthCheck(); status.Code(err) != codes.Unavailable {
		t.Fatalf("node with a different certificate: %v", err)
	}
	client := gost.NewClient(&gost.Config{Address: controlAddress, CertificatePEM: nodeCert, Username: username, Password: password})
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
