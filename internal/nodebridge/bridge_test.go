package nodebridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
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
	"google.golang.org/protobuf/proto"
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
		reports:  make(chan *nodegrpc.ReportRequest, 2),
		sessions: make(chan *nodegrpc.Session, 2),
	}
	var attempts atomic.Int32
	limitedAt := make(chan time.Time, 1)
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 || r.URL.Path != nodegrpc.SessionPath {
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
		recorder.ServeHTTP(w, r)
	}))
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	panelCertFile := filepath.Join(t.TempDir(), "panel-ca.pem")
	if err := os.WriteFile(panelCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}

	observerListener := listenLocal(t)
	observerAddress := observerListener.Addr().String()
	_ = observerListener.Close()
	configPath := preparePanelCredentials(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Config{
			NodeID: 42, Username: username, Password: password, ObserverListen: observerAddress,
			AdminAddress: strings.TrimPrefix(admin.URL, "http://"), Host: "127.0.0.1",
			ReportCAFile: panelCertFile, ConfigFile: configPath,
		}, dialPanelAt(proxy.Listener))
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
		if _, err := panelClient.GetNodeConfig(timeout, &emptypb.Empty{}); status.Code(err) != codes.Unimplemented {
			t.Fatalf("node configuration RPC in reverse direction: %v", err)
		}
		for {
			result, err := localObserver.Observe(withToken, observation)
			if err == nil && result.GetOk() {
				break
			}
			if status.Code(err) != codes.Unavailable || timeout.Err() != nil {
				t.Fatalf("observe on connection %d: %v, %v", attempt, result, err)
			}
			time.Sleep(time.Millisecond)
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
	for {
		if template, err := ReadNftTemplate(configPath); err == nil && template == `{"nftables":[]}` {
			break
		}
		select {
		case <-timeout.Done():
			t.Fatal("node configuration was not cached")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestNodeBridgeReconnectsWithPinnedPanelCertificate(t *testing.T) {
	const username, password = "node", "test-password"
	recorder := &reportRecorder{sessions: make(chan *nodegrpc.Session, 2)}
	https := httptest.NewTLSServer(recorder)
	t.Cleanup(https.Close)
	digest := sha256.Sum256(https.Certificate().Raw)
	configPath := preparePanelCredentials(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Config{
			NodeID: 42, Username: username, Password: password,
			AdminAddress: "127.0.0.1:1", ObserverListen: "127.0.0.1:0",
			Host: "127.0.0.1", ReportCertSHA256: hex.EncodeToString(digest[:]), ConfigFile: configPath,
		}, dialPanelAt(https.Listener))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	timeout, stop := context.WithTimeout(t.Context(), 12*time.Second)
	defer stop()
	for i := 0; i < 2; i++ {
		select {
		case session := <-recorder.sessions:
			session.Close()
		case <-timeout.Done():
			t.Fatalf("pinned connection %d did not reach panel: %v", i+1, timeout.Err())
		}
	}
}

func TestNodeBridgeRefreshesWithoutReplacingSession(t *testing.T) {
	recorder := &reportRecorder{sessions: make(chan *nodegrpc.Session, 2)}
	refreshed := make(chan struct{}, 1)
	var sessions atomic.Int32
	panel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nodegrpc.RefreshPath {
			sessions.Add(1)
			recorder.ServeHTTP(w, r)
			return
		}
		data, err := io.ReadAll(r.Body)
		request := new(nodegrpc.RefreshNodeRequest)
		if err != nil || proto.Unmarshal(data, request) != nil || request.GetRefreshToken() != testAccessToken+"-refresh" {
			t.Error("online node did not send its refresh credential")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeTestProtobuf(t, w, testCredentials(time.Now().Add(15*time.Minute), "new-access"))
		refreshed <- struct{}{}
	}))
	t.Cleanup(panel.Close)
	configPath := filepath.Join(t.TempDir(), "node.yaml")
	data, err := EncodePanelCredentials(testCredentials(time.Now().Add(refreshLeadTime+3*time.Second), testAccessToken))
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(filepath.Dir(configPath), PanelCredentialsFile)
	if err = WriteNodeFile(credentialPath, data); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(panel.Certificate().Raw)
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Config{
			NodeID: 42, Username: "node", Password: "test-password",
			AdminAddress: "127.0.0.1:1", ObserverListen: "127.0.0.1:0",
			Host: "127.0.0.1", ReportCertSHA256: hex.EncodeToString(digest[:]), ConfigFile: configPath,
		}, dialPanelAt(panel.Listener))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	var session *nodegrpc.Session
	select {
	case session = <-recorder.sessions:
	case <-ctx.Done():
		t.Fatal("node failed to connect")
	}
	select {
	case <-refreshed:
	case <-ctx.Done():
		t.Fatal("online node did not refresh")
	}
	for {
		stored, err := os.ReadFile(credentialPath)
		if err == nil && strings.Contains(string(stored), "access_token: new-access") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("online node did not persist its refreshed credentials")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := nodegrpc.NewV1Client(session).GetObserverEndpoint(ctx, &emptypb.Empty{}); err != nil || sessions.Load() != 1 {
		t.Fatalf("credential refresh replaced the original session: %d attempts, %v", sessions.Load(), err)
	}
}

func TestNodeBridgeRetriesConfigurationBeforePublishingSession(t *testing.T) {
	recorder := &reportRecorder{sessions: make(chan *nodegrpc.Session, 2)}
	recorder.failConfig.Store(true)
	panel := httptest.NewTLSServer(recorder)
	t.Cleanup(panel.Close)
	client := panel.Client()
	client.Transport.(*http.Transport).DialContext = dialPanelAt(panel.Listener)
	configPath := preparePanelCredentials(t)
	host, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := openCredentialManager(Config{NodeID: 42, ConfigFile: configPath}, host, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var current atomic.Pointer[nodegrpc.Session]
	connected, err := runPanelSession(ctx, credentials, configPath, &current, nil)
	if connected || status.Code(errors.Unwrap(err)) != codes.Unavailable || current.Load() != nil {
		t.Fatalf("failed config unexpectedly published the session: %v, %v", connected, err)
	}
	if _, err = ReadNftTemplate(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed config left a snapshot: %v", err)
	}
	go maintainPanelChannel(ctx, credentials, configPath, &current, nil)
	for current.Load() == nil {
		select {
		case <-ctx.Done():
			t.Fatal("retry never published the session")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err = ReadNftTemplate(configPath); err != nil {
		t.Fatalf("session published before saving configuration: %v", err)
	}
}

const testAccessToken = "access-token"

type reportRecorder struct {
	nodegrpc.UnimplementedV1Server
	reports    chan *nodegrpc.ReportRequest
	sessions   chan *nodegrpc.Session
	failConfig atomic.Bool
}

func (s *reportRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testAccessToken || r.Header.Get(nodegrpc.NodeIDMetadata) != "42" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	stream, err := nodegrpc.AcceptSession(w, r)
	if err != nil {
		return
	}
	defer stream.Close()
	session := nodegrpc.NewSession(stream, nodegrpc.NewDispatcher(s,
		nodegrpc.V1_Report_FullMethodName, nodegrpc.V1_UpdateNodeIPs_FullMethodName, nodegrpc.V1_GetNodeConfig_FullMethodName))
	s.sessions <- session
	_ = session.Run()
}

func (s *reportRecorder) GetNodeConfig(context.Context, *emptypb.Empty) (*nodegrpc.NodeConfig, error) {
	if s.failConfig.CompareAndSwap(true, false) {
		return nil, status.Error(codes.Unavailable, "temporary configuration failure")
	}
	return &nodegrpc.NodeConfig{NftTemplate: `{"nftables":[]}`, ConfigYaml: []byte("policy: default")}, nil
}

func preparePanelCredentials(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.yaml")
	data, err := EncodePanelCredentials(testCredentials(time.Now().Add(10*time.Minute), testAccessToken))
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteNodeFile(filepath.Join(filepath.Dir(path), PanelCredentialsFile), data); err != nil {
		t.Fatal(err)
	}
	return path
}

func testCredentials(accessExpiry time.Time, access string) *nodegrpc.NodeCredentials {
	return &nodegrpc.NodeCredentials{
		AccessToken: access, RefreshToken: access + "-refresh",
		AccessExpiresAt: accessExpiry.Unix(), RefreshExpiresAt: accessExpiry.Add(time.Hour).Unix(),
	}
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

func dialPanelAt(listener net.Listener) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	}
}

func writeTestProtobuf(t *testing.T, w http.ResponseWriter, message proto.Message) {
	t.Helper()
	data, err := proto.Marshal(message)
	if err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", nodegrpc.ProtobufContentType)
	if _, err = w.Write(data); err != nil {
		t.Error(err)
	}
}
