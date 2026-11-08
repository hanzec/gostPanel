package nodebridge

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const controlTestUser, controlTestPassword = "node", "control-test-password"

func controlTestContext() context.Context {
	token := base64.StdEncoding.EncodeToString([]byte(controlTestUser + ":" + controlTestPassword))
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Basic "+token))
}

func TestControlCommandScope(t *testing.T) {
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/config", true},
		{"POST", "/config", false},
		{"POST", "/config?format=yaml", true},
		{"GET", "/config?format=yaml", false},
		{"GET", "/config?format=json", false},
		{"POST", "/config/services", true},
		{"DELETE", "/config/services", false},
		{"GET", "/config/limiter/not-allowed", false},
		{"GET", "/config/services/item%2fother", false},
		{"GET", "/config/services/with.dot-1", true},
		{"DELETE", "/config/services/with.dot-1", true},
		{"PUT", "/config/services/relay-tunnel-1", true},
		{"PUT", "/config/services/other", false},
		{"PUT", "/config/observers/other", true},
		{"PUT", "/config/limiters/other", false},
		{"GET", "/config/admissions/other", true},
		{"PUT", "/config/bypasses/other", true},
		{"DELETE", "/config/bypasses/other", false},
	} {
		if got := allowedCommand(test.method, test.path); got != test.allowed {
			t.Errorf("%s %s allowed = %t, want %t", test.method, test.path, got, test.allowed)
		}
	}
}

func TestControlExecuteProxy(t *testing.T) {
	const payload = "{\"services\":[]}"
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != controlTestUser || password != controlTestPassword {
			t.Errorf("unexpected credentials on local request")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		switch r.URL.Path {
		case "/api/config/services":
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != payload || r.Method != "POST" {
				t.Errorf("proxied request = %s %q, %v", r.Method, body, err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, "created")
		case "/api/config/services/truncated":
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "truncated")
		case "/api/config/services/oversized":
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
		default:
			t.Errorf("unexpected proxied route %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer admin.Close()
	control := controlServer{config: Config{
		Username: controlTestUser, Password: controlTestPassword, AdminAddress: strings.TrimPrefix(admin.URL, "http://"),
	}, client: admin.Client()}
	ctx := controlTestContext()
	reply, err := control.Execute(ctx, &nodegrpc.CommandRequest{Method: "POST", Path: "/config/services", Body: []byte(payload)})
	if err != nil || reply.GetStatusCode() != http.StatusCreated || string(reply.GetBody()) != "created" {
		t.Fatalf("proxy result = %v, %v", reply, err)
	}
	for _, test := range []struct {
		path string
		code codes.Code
	}{
		{"/config/services/truncated", codes.Unavailable},
		{"/config/services/oversized", codes.ResourceExhausted},
		{"/config/services/item/extra", codes.InvalidArgument},
	} {
		_, err := control.Execute(ctx, &nodegrpc.CommandRequest{Method: "GET", Path: test.path})
		if status.Code(err) != test.code {
			t.Errorf("GET %s = %v, want %s", test.path, err, test.code)
		}
	}
	admin.Close()
	if _, err := control.Execute(ctx, &nodegrpc.CommandRequest{Method: "GET", Path: "/config"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("unreachable local API returned %v", err)
	}
	control.config.AdminAddress = "bad host"
	if _, err := control.Execute(ctx, &nodegrpc.CommandRequest{Method: "GET", Path: "/config"}); status.Code(err) != codes.Internal {
		t.Fatalf("malformed local API address returned %v", err)
	}
}

func TestBridgeRejectsInvalidConfiguration(t *testing.T) {
	cert, key, _ := testTLSFiles(t, x509.Certificate{DNSNames: []string{nodegrpc.NodeCertificateName}})
	listener := listenLocal(t)
	occupied := listener.Addr().String()
	valid := Config{
		NodeID: 42, Username: controlTestUser, Password: controlTestPassword,
		AdminAddress: "127.0.0.1:1", ReportAddress: occupied, ObserverListen: "127.0.0.1:0",
		ControlListen: "127.0.0.1:0", TLSCertFile: cert, TLSKeyFile: key,
	}
	for _, test := range []struct {
		name, want string
		change     func(*Config)
	}{
		{"credentials", "node ID and credentials", func(cfg *Config) { cfg.Username = "node:invalid" }},
		{"admin address", "loopback", func(cfg *Config) { cfg.AdminAddress = "192.0.2.1:80" }},
		{"report address", "host and port", func(cfg *Config) { cfg.ReportAddress = ":1" }},
		{"node certificate", "load node TLS certificate", func(cfg *Config) { cfg.TLSCertFile = key }},
		{"panel CA", "create panel report client", func(cfg *Config) { cfg.ReportCAFile = key }},
		{"occupied control", "listen for node control", func(cfg *Config) { cfg.ControlListen = occupied }},
		{"occupied observer", "listen for local Gost observer", func(cfg *Config) { cfg.ObserverListen = occupied }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.change(&cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() = %v, want %q", err, test.want)
			}
		})
	}
	t.Run("optional state does not prevent startup", func(t *testing.T) {
		cfg := valid
		dir := t.TempDir()
		cfg.NftConfigFile = dir
		cfg.ConfigFile = filepath.Join(dir, "node.yaml")
		if err := os.WriteFile(trafficQuotaPath(cfg.ConfigFile), []byte("malformed quota state"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Run(ctx, cfg); err != nil {
			t.Fatalf("bridge failed with unreadable optional state: %v", err)
		}
	})
}

func TestNftConfigurationRPC(t *testing.T) {
	ctx := controlTestContext()
	dir := t.TempDir()
	control := controlServer{config: Config{
		NodeID: 42, Username: controlTestUser, Password: controlTestPassword, NftConfigFile: filepath.Join(dir, "nft.json"),
	}}
	if _, err := control.GetNftConfig(context.Background(), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated config request returned %v", err)
	}
	if result, err := control.GetNftConfig(ctx, &emptypb.Empty{}); err != nil || result.GetConfig() != "" {
		t.Fatalf("missing config = %v, %v", result, err)
	}
	config := "{\"nftables\":[]}"
	if err := os.WriteFile(control.config.NftConfigFile, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := control.GetNftConfig(ctx, &emptypb.Empty{}); err != nil || result.GetConfig() != config {
		t.Fatalf("saved config = %v, %v", result, err)
	}
	control.config.NftConfigFile = dir
	if _, err := control.GetNftConfig(ctx, &emptypb.Empty{}); status.Code(err) != codes.Internal {
		t.Fatalf("unreadable config returned %v", err)
	}
	if err := restoreNftConfig(ctx, 42, dir); err == nil {
		t.Fatal("restoring a directory as config succeeded")
	}
	control.config.NftConfigFile = ""
	if _, err := control.ApplyNftConfig(ctx, &nodegrpc.NftConfig{Config: config}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("config update on unconfigured node returned %v", err)
	}
}

type nftTemplatePanel struct {
	nodegrpc.UnimplementedPanelServer
}

func (nftTemplatePanel) GetNftTemplate(ctx context.Context, request *nodegrpc.NftTemplateRequest) (*nodegrpc.NftConfig, error) {
	if !nodegrpc.MatchCredentials(ctx, controlTestUser, controlTestPassword) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	if request.GetNodeId() != 42 {
		return nil, status.Error(codes.NotFound, "unknown node")
	}
	return &nodegrpc.NftConfig{Config: "{\"nftables\":[]}"}, nil
}

func TestGetNftTemplateAuthenticatesToPanel(t *testing.T) {
	certFile, keyFile, _ := testTLSFiles(t, x509.Certificate{IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}})
	serverTLS, err := credentials.NewServerTLSFromFile(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(serverTLS))
	t.Cleanup(server.Stop)
	nodegrpc.RegisterPanelServer(server, nftTemplatePanel{})
	listener := listenLocal(t)
	go func() { _ = server.Serve(listener) }()
	cfg := Config{NodeID: 42, Username: controlTestUser, Password: controlTestPassword,
		ReportAddress: listener.Addr().String(), ReportCAFile: certFile}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got, err := GetNftTemplate(ctx, cfg); err != nil || got != "{\"nftables\":[]}" {
		t.Fatalf("template = %q, %v", got, err)
	}
	cfg.NodeID = 1
	if _, err := GetNftTemplate(ctx, cfg); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown node returned %v", err)
	}
	cfg.ReportCAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := GetNftTemplate(ctx, cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing CA returned %v", err)
	}
}

func TestProbePublicIPResponseValidation(t *testing.T) {
	for _, test := range []struct {
		name, response, want string
		status               int
		truncated            bool
	}{
		{"valid", " 203.0.113.50\n", "203.0.113.50", http.StatusOK, false},
		{"private", "10.1.2.3", "", http.StatusOK, false},
		{"wrong family", "2001:db8::1", "", http.StatusOK, false},
		{"mapped address", "::ffff:203.0.113.50", "", http.StatusOK, false},
		{"status error", "203.0.113.50", "", http.StatusBadGateway, false},
		{"redirect", "203.0.113.50", "", http.StatusTemporaryRedirect, false},
		{"too long", strings.Repeat(" ", 65) + "203.0.113.50", "", http.StatusOK, false},
		{"truncated", "203.0.113.50", "", http.StatusOK, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					t.Error("followed a redirect from the public IP endpoint")
					_, _ = io.WriteString(w, test.response)
					return
				}
				if test.truncated {
					w.Header().Set("Content-Length", fmt.Sprint(len(test.response)+10))
				}
				if test.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", "/target")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			}))
			defer admin.Close()
			if got := probePublicIP(context.Background(), "tcp4", admin.URL); got != test.want {
				t.Fatalf("probePublicIP() = %q, want %q", got, test.want)
			}
		})
	}
	if got := probePublicIP(context.Background(), "tcp4", "://invalid"); got != "" {
		t.Fatalf("malformed endpoint returned %q", got)
	}
}
