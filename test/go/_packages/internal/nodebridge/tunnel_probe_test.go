package nodebridge

import (
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
	"testing"
	"time"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

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
	}{
		{"plain echo", sha256.Size, false, codes.DataLoss},
		{"truncated reply", 1, false, codes.Unavailable},
		{"no reply before deadline", 0, true, codes.DeadlineExceeded},
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
			if _, err := exchangeTunnelProbe(ctx, client, endpoint.GetToken()); status.Code(err) != tc.want {
				t.Fatalf("exchangeTunnelProbe returned %v, want %s", err, tc.want)
			}
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
	}{
		{name: "missing chain", response: `{"data":null}`, want: codes.NotFound},
		{name: "wrong chain", response: `{"data":{"name":"tunnel-2-chain"}}`, want: codes.FailedPrecondition},
		{name: "Gost admin is offline", adminOffline: true, want: codes.Internal},
		{name: "Gost denies admin access", httpStatus: http.StatusUnauthorized, want: codes.Internal},
		{name: "invalid Gost admin response", response: "not JSON", want: codes.Internal},
		{name: "missing Gost binary", response: liveChain, want: codes.Internal},
		{name: "Gost exits during startup", response: liveChain, failingProcess: true, want: codes.Internal},
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
			if status.Code(err) != tc.want {
				t.Fatalf("ProbeTunnel returned %v, want %s", err, tc.want)
			}
		})
	}
}

func TestTunnelProbeThroughGostRelay(t *testing.T) {
	binary := os.Getenv("YAGP_TEST_GOST_BINARY")
	if binary == "" || runtime.GOOS != "linux" {
		t.Skip("set YAGP_TEST_GOST_BINARY to an installed Linux Gost binary")
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
		Handler:  &gost.HandlerConfig{Type: "relay", Auth: chain.Hops[0].Nodes[0].Connector.Auth},
	}
	startTunnelProbeTestRelay(t, binary, dir, relay, chain, control)
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
	reply, err := control.Execute(context.Background(), &nodegrpc.CommandRequest{
		Method: http.MethodDelete, Path: "/config/services/" + relay.Name,
	})
	if err != nil || reply.GetStatusCode() != http.StatusOK {
		t.Fatalf("delete live Gost Relay: %v %s", err, reply.GetBody())
	}
	if _, err := probe(); status.Code(err) != codes.Unavailable {
		t.Fatalf("probe through stopped Gost Relay returned %v, want Unavailable", err)
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
