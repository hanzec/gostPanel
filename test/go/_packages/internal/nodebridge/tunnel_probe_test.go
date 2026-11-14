package nodebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestTunnelProbeVerifiesResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	control := &controlServer{}
	endpoint, err := control.StartTunnelProbe(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint.GetAddress())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := exchangeTunnelProbe(ctx, conn, endpoint.GetToken()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		replyBytes int
		keepOpen   bool
		want       codes.Code
		stage      string
	}{
		{"plain echo", sha256.Size, false, codes.DataLoss, "relay_response"},
		{"truncated reply", 1, false, codes.Unavailable, "relay_read"},
		{"no reply before deadline", 0, true, codes.DeadlineExceeded, "relay_read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			client, server := net.Pipe()
			defer client.Close()
			go func() {
				defer server.Close()
				var request [sha256.Size]byte
				if _, err := io.ReadFull(server, request[:]); err != nil {
					return
				}
				if tc.keepOpen {
					<-ctx.Done()
					return
				}
				_, _ = server.Write(request[:tc.replyBytes])
			}()
			_, err := exchangeTunnelProbe(ctx, client, endpoint.GetToken())
			requireTunnelProbeFailure(t, err, tc.want, tc.stage)
		})
	}
}

func TestTunnelProbeRejectsUnmanagedDestination(t *testing.T) {
	control := &controlServer{}
	for _, req := range []*nodegrpc.TunnelProbeRequest{
		{ChainName: "tunnel-1-chain", Address: "192.0.2.1:12345", Token: make([]byte, sha256.Size)},
		{ChainName: "tunnel-1-clash-chain", Address: "127.0.0.1:12345", Token: make([]byte, sha256.Size)},
		{ChainName: "tunnel-1-chain", Address: "127.0.0.1:12345", Token: []byte("invalid")},
	} {
		if _, err := control.ProbeTunnel(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request %+v returned %v, want InvalidArgument", req, err)
		}
	}
}

func TestTunnelProbeReportsMissingOrInvalidGostSetup(t *testing.T) {
	const liveChain = `{"data":{"name":"tunnel-1-chain","hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":"192.0.2.1:8443","connector":{"type":"relay"},"dialer":{"type":"tcp"}}]}]}}`
	for _, tc := range []struct {
		name, response string
		httpStatus     int
		adminOffline   bool
		failingProcess bool
		want           codes.Code
		stage          string
		gostFailure    string
	}{
		{name: "missing chain", response: `{"data":null}`, want: codes.NotFound, stage: "entry_chain"},
		{name: "wrong chain", response: `{"data":{"name":"tunnel-2-chain"}}`, want: codes.FailedPrecondition, stage: "entry_chain"},
		{name: "Gost admin is offline", adminOffline: true, want: codes.Internal, stage: "entry_gost_api"},
		{name: "Gost denies admin access", httpStatus: http.StatusUnauthorized, want: codes.Internal, stage: "entry_gost_api", gostFailure: "admin_authentication"},
		{name: "invalid Gost admin response", response: "not JSON", want: codes.Internal, stage: "entry_gost_api"},
		{name: "missing Gost binary", response: liveChain, want: codes.Internal, stage: "entry_gost_process", gostFailure: "binary_missing"},
		{name: "Gost exits during startup", response: liveChain, failingProcess: true, want: codes.Internal, stage: "entry_gost_process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.failingProcess {
				if runtime.GOOS != "linux" {
					t.Skip("requires a Linux executable")
				}
				binary, err := exec.LookPath("false")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(binary, filepath.Join(dir, "gost")); err != nil {
					t.Fatal(err)
				}
			}
			admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/config/chains/tunnel-1-chain" {
					t.Errorf("unexpected Gost admin request %s %s", r.Method, r.URL.Path)
				}
				if tc.httpStatus != 0 {
					w.WriteHeader(tc.httpStatus)
				}
				_, _ = io.WriteString(w, tc.response)
			}))
			defer admin.Close()
			control := &controlServer{config: Config{
				AdminAddress: admin.Listener.Addr().String(), ConfigFile: filepath.Join(dir, "node.yaml"),
			}, client: admin.Client()}
			if tc.adminOffline {
				admin.Close()
			}
			_, err := control.ProbeTunnel(context.Background(), &nodegrpc.TunnelProbeRequest{
				ChainName: "tunnel-1-chain", Address: "127.0.0.1:12345", Token: make([]byte, sha256.Size),
			})
			info := requireTunnelProbeFailure(t, err, tc.want, tc.stage)
			if got := info.GetMetadata()["gost_error"]; got != tc.gostFailure {
				t.Fatalf("Gost failure = %q, want %q", got, tc.gostFailure)
			}
		})
	}
}

func requireTunnelProbeFailure(t *testing.T, err error, code codes.Code, stages ...string) *errdetails.ErrorInfo {
	t.Helper()
	st := status.Convert(err)
	if st.Code() != code {
		t.Fatalf("probe returned %v, want %s", err, code)
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "yagp.tunnel_probe" && slices.Contains(stages, info.GetReason()) {
			return info
		}
	}
	t.Fatalf("probe failure has details %v, want one of %v", st.Details(), stages)
	return nil
}

func TestTunnelProbeLogClassifiesOnlyKnownPrivateProcessErrors(t *testing.T) {
	var log tunnelProbeLog
	for _, chunk := range []string{
		string(bytes.Repeat([]byte{'x'}, 5000)),
		`{"level":"debug","service":"yagp-tunnel-probe","msg":"pipe: 2 Unauthorized"}` + "\n",
		`{"level":"debug","service":"other","msg":"pipe: 2 Unauthorized"}` + "\n",
		`{"level":"debug","service":"yagp-tunnel-probe","msg":"pipe: 3 For`,
		`bidden","authorization":"sensitive value"}` + "\n",
	} {
		if n, err := io.WriteString(&log, chunk); err != nil || n != len(chunk) {
			t.Fatalf("log accepted %d/%d bytes, error %v", n, len(chunk), err)
		}
	}
	if log.failure != "relay_target_forbidden" {
		t.Fatalf("Gost failure = %q, want relay_target_forbidden", log.failure)
	}
}

func TestTunnelProbeTCPDiagnosticSkipsDatagramTransports(t *testing.T) {
	for _, transport := range []string{"udp", "kcp", "quic", "h3"} {
		relay := &gost.NodeConfig{Addr: "127.0.0.1:0", Dialer: &gost.DialerConfig{Type: transport}}
		if got := checkTunnelRelayTCP(t.Context(), relay); got != "not_applicable" {
			t.Errorf("TCP diagnostic for %s = %q, want not_applicable", transport, got)
		}
	}
}

func TestTunnelProbeThroughGostRelay(t *testing.T) {
	binary := os.Getenv("YAGP_TEST_GOST_BINARY")
	if binary == "" || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") {
		t.Skip("set YAGP_TEST_GOST_BINARY to an installed Linux or macOS Gost binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	// The production daemon resolves Gost relative to its own config file.
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "gost")); err != nil {
		t.Fatal(err)
	}
	relayAddress, adminAddress := tunnelProbeTestAddresses(t)
	chain := gost.ChainConfig{Name: "tunnel-1-chain", Hops: []*gost.HopConfig{{Name: "hop-0", Nodes: []*gost.NodeConfig{{
		Name: "exit-relay", Addr: relayAddress,
		Connector: &gost.ConnectorConfig{Type: "relay", Auth: &gost.AuthConfig{Username: "probe", Password: "relay-password"}},
		Dialer:    gost.RelayDialer("ws", map[string]string{"path": "/probe"}),
	}}}}}
	control := &controlServer{config: Config{
		AdminAddress: adminAddress, ConfigFile: filepath.Join(dir, "node.yaml"),
		Username: "node", Password: "node-password",
	}, client: &http.Client{Timeout: time.Second}}
	defer control.client.CloseIdleConnections()

	relay := gost.ServiceConfig{Name: "relay-tunnel-1", Addr: relayAddress,
		Listener: gost.RelayListener("ws", map[string]string{"path": "/probe"}),
		Handler:  &gost.HandlerConfig{Type: "relay", Auth: &gost.AuthConfig{Username: "probe", Password: "relay-password"}},
	}
	startTunnelProbeTestRelay(t, binary, dir, relay, chain, control)
	writeConfig := func(t *testing.T, method, path string, config any) {
		t.Helper()
		body, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := control.Execute(t.Context(), &nodegrpc.CommandRequest{Method: method, Path: path, Body: body})
		if err != nil || reply.GetStatusCode() != http.StatusOK {
			t.Fatalf("update live Gost configuration at %s: %v %s", path, err, reply.GetBody())
		}
	}
	probe := func() (*nodegrpc.TunnelProbeReply, error) {
		endpoint, err := control.StartTunnelProbe(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		return control.ProbeTunnel(context.Background(), &nodegrpc.TunnelProbeRequest{
			ChainName: chain.Name, Address: endpoint.GetAddress(), Token: endpoint.GetToken(),
		})
	}
	if _, err := probe(); err != nil {
		t.Fatalf("probe through live Gost Relay: %v", err)
	}
	node := chain.Hops[0].Nodes[0]
	for _, tc := range []struct {
		name, password, path, failure string
	}{
		{"wrong relay credentials", "wrong-password", "/probe", "relay_authentication"},
		{"wrong websocket path", "relay-password", "/wrong-path", "relay_handshake"},
		{"restore relay settings", "relay-password", "/probe", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node.Connector.Auth.Password = tc.password
			node.Dialer = gost.RelayDialer("ws", map[string]string{"path": tc.path})
			writeConfig(t, http.MethodPut, "/config/chains/"+chain.Name, chain)
			_, err := probe()
			if tc.failure == "" {
				if err != nil {
					t.Fatalf("probe through restored Gost Relay: %v", err)
				}
				return
			}
			info := requireTunnelProbeFailure(t, err, codes.Unavailable, "relay_read")
			if info.GetMetadata()["gost_error"] != tc.failure || info.GetMetadata()["relay_tcp"] != "reachable" {
				t.Fatalf("unexpected diagnostics %v, want %s and TCP reachable", info.GetMetadata(), tc.failure)
			}
		})
	}
	unavailable, _ := tunnelProbeTestAddresses(t)
	_, err = control.ProbeTunnel(t.Context(), &nodegrpc.TunnelProbeRequest{
		ChainName: chain.Name, Address: unavailable, Token: make([]byte, sha256.Size),
	})
	info := requireTunnelProbeFailure(t, err, codes.Unavailable, "relay_read")
	if info.GetMetadata()["gost_error"] != "relay_target_unreachable" || info.GetMetadata()["relay_tcp"] != "reachable" {
		t.Fatalf("unexpected diagnostics for closed test target: %v", info.GetMetadata())
	}
	t.Run("source admission and target policy", func(t *testing.T) {
		relay.Admission = gost.TunnelRelayAllowlistName(relay.Name)
		relay.Bypass = "tunnel-1-bypass"
		relay.Listener = gost.RelayListener("tcp", nil)
		node.Dialer = gost.RelayDialer("tcp", nil)
		admission := gost.AdmissionConfig{Name: relay.Admission, Whitelist: true, Matchers: []string{"127.0.0.1"}}
		writeConfig(t, http.MethodPost, "/config/admissions", admission)
		writeConfig(t, http.MethodPost, "/config/bypasses", map[string]any{
			"name": relay.Bypass, "whitelist": true, "matchers": []string{"example.com"},
		})
		writeConfig(t, http.MethodPut, "/config/services/"+relay.Name, relay)
		writeConfig(t, http.MethodPut, "/config/chains/"+chain.Name, chain)

		_, err := probe()
		info := requireTunnelProbeFailure(t, err, codes.Unavailable, "relay_read")
		if info.GetMetadata()["gost_error"] != "relay_target_forbidden" || info.GetMetadata()["relay_tcp"] != "reachable" {
			t.Fatalf("unexpected diagnostics for an allowed source and forbidden target: %v", info.GetMetadata())
		}

		admission.Matchers = []string{"192.0.2.1"}
		writeConfig(t, http.MethodPut, "/config/admissions/"+admission.Name, admission)
		_, err = probe()
		info = requireTunnelProbeFailure(t, err, codes.Unavailable, "relay_write", "relay_read")
		if info.GetMetadata()["gost_error"] != "" || info.GetMetadata()["relay_tcp"] != "reachable" {
			t.Fatalf("unexpected diagnostics for a denied source: %v", info.GetMetadata())
		}
	})
	reply, err := control.Execute(context.Background(), &nodegrpc.CommandRequest{
		Method: http.MethodDelete, Path: "/config/services/" + relay.Name,
	})
	if err != nil || reply.GetStatusCode() != http.StatusOK {
		t.Fatalf("delete live Gost Relay: %v %s", err, reply.GetBody())
	}
	_, err = probe()
	// Gost may close the local probe socket before or after the token is written.
	info = requireTunnelProbeFailure(t, err, codes.Unavailable, "relay_write", "relay_read")
	if got := info.GetMetadata()["relay_tcp"]; got != "refused" {
		t.Fatalf("relay TCP check = %q, want refused", got)
	}
}

func tunnelProbeTestAddresses(t *testing.T) (string, string) {
	t.Helper()
	var addresses [2]string
	for i := range addresses {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		addresses[i] = listener.Addr().String()
	}
	return addresses[0], addresses[1]
}

func startTunnelProbeTestRelay(t *testing.T, binary, dir string, relay gost.ServiceConfig, chain gost.ChainConfig, control *controlServer) {
	t.Helper()
	config, err := json.Marshal(map[string]any{
		"services": []gost.ServiceConfig{relay},
		"chains":   []gost.ChainConfig{chain},
		"api": map[string]any{
			"addr": control.config.AdminAddress, "pathPrefix": "/api",
			"auth": gost.AuthConfig{Username: control.config.Username, Password: control.config.Password},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "relay.json")
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-C", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		reply, err := control.Execute(ctx, &nodegrpc.CommandRequest{Method: http.MethodGet, Path: "/config"})
		if err == nil && reply.GetStatusCode() == http.StatusOK {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("relay did not start: %v", err)
		case <-done:
			t.Fatalf("relay exited during startup: %v", err)
		case <-ticker.C:
		}
	}
}
