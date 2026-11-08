package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"yagp/internal/nodebridge"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const enrollmentTestHost = "127.0.0.1"

type enrollmentPanel struct {
	nodegrpc.UnimplementedV1Server
	register func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error)
}

func (p enrollmentPanel) RegisterNode(_ context.Context, req *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
	return p.register(req)
}

func (p enrollmentPanel) GetNftTemplate(_ context.Context, req *nodegrpc.NftTemplateRequest) (*nodegrpc.NftConfig, error) {
	if req.NodeId != 42 {
		return nil, status.Error(codes.NotFound, "unknown node")
	}
	return &nodegrpc.NftConfig{Config: `{"nftables":[]}`}, nil
}

func testPanel(t *testing.T, panel enrollmentPanel) (string, grpc.DialOption) {
	t.Helper()
	https := httptest.NewTLSServer(nil)
	cert := https.TLS.Certificates[0]
	https.Close()
	caFile := filepath.Join(t.TempDir(), "panel.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	if err := os.WriteFile(caFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})))
	nodegrpc.RegisterPanelServer(server, panel)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return caFile, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	})
}

func TestRegisterNodeWritesEnrolledCredentials(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	detect := func(context.Context) (string, string) { return "192.0.2.15", "2001:db8::15" }
	panel := enrollmentPanel{register: func(req *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
		if req.Token != token || req.Ipv4 != "192.0.2.15" || req.Ipv6 != "2001:db8::15" {
			t.Errorf("unexpected registration token or addresses: %s %s", req.Ipv4, req.Ipv6)
		}
		return &nodegrpc.RegisterNodeReply{NodeId: 42, Username: "registered", Password: "test-secret"}, nil
	}}
	ca, dial := testPanel(t, panel)
	t.Setenv("YAGP_PANEL_CA_FILE", ca)
	output := t.TempDir()
	id, err := registerNode(ctx, enrollmentTestHost, token, stateDir, output, detect, dial)
	if err != nil || id != 42 {
		t.Fatalf("enrollment = %d, %v", id, err)
	}
	configPath := filepath.Join(output, "node.yaml")
	cfg, err := readConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != id || cfg.Username != "registered" || cfg.Password != "test-secret" ||
		cfg.Host != enrollmentTestHost || cfg.ReportCAFile != ca || cfg.ObserverListen == cfg.AdminAddress ||
		!nodebridge.IsLoopbackAddress(cfg.AdminAddress) || !nodebridge.IsLoopbackAddress(cfg.ObserverListen) {
		t.Fatalf("invalid staged node configuration: %+v", cfg)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 2 || entries[0].Name() != "gost.yaml" || entries[1].Name() != "node.yaml" {
		t.Fatalf("unexpected staged files: %v, %v", entries, err)
	}
	gost, err := os.ReadFile(filepath.Join(output, "gost.yaml"))
	if err != nil || !strings.Contains(string(gost), cfg.AdminAddress) || !strings.Contains(string(gost), "pathPrefix: /api") {
		t.Fatalf("invalid Gost configuration: %s, %v", gost, err)
	}
	if err := printNftTemplate(ctx, configPath, dial); err != nil {
		t.Fatal(err)
	}
	if _, err := registerNode(ctx, enrollmentTestHost, token, stateDir, output, detect, dial); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("re-enrollment overwrote staged files: %v", err)
	}
}

func TestRegisterNodePinsGeneratedPanelCertificate(t *testing.T) {
	var calls atomic.Int32
	caFile, dial := testPanel(t, enrollmentPanel{register: func(req *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
		calls.Add(1)
		return &nodegrpc.RegisterNodeReply{NodeId: 42, Username: "registered", Password: "test-secret"}, nil
	}})
	data, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	digest := sha256.Sum256(block.Bytes)
	fingerprint := hex.EncodeToString(digest[:])
	baseToken := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("YAGP_PANEL_CA_FILE", filepath.Join(t.TempDir(), "unused-ca.pem"))
	stateDir, output := t.TempDir(), t.TempDir()
	detect := func(context.Context) (string, string) { return "", "" }
	if _, err := registerNode(t.Context(), enrollmentTestHost, baseToken+"."+strings.Repeat("0", 64), stateDir, output, detect, dial); err == nil ||
		!strings.Contains(err.Error(), "fingerprint mismatch") || calls.Load() != 0 {
		t.Fatalf("invalid fingerprint reached enrollment: %v, %d requests", err, calls.Load())
	}
	id, err := registerNode(t.Context(), enrollmentTestHost, baseToken+"."+fingerprint, stateDir, output, detect, dial)
	if err != nil || id != 42 || calls.Load() != 1 {
		t.Fatalf("enrollment with pin = %d, %v, %d requests", id, err, calls.Load())
	}
	configPath := filepath.Join(output, "node.yaml")
	config, err := readConfig(configPath)
	if err != nil || config.Host != enrollmentTestHost || config.ReportCertSHA256 != fingerprint || config.ReportCAFile != "" {
		t.Fatalf("staged certificate identity = %q, %q, %v", config.ReportCertSHA256, config.ReportCAFile, err)
	}
	if err := printNftTemplate(t.Context(), configPath, dial); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterNodeRejectsInvalidInputAndPanelReply(t *testing.T) {
	ctx := context.Background()
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	detect := func(context.Context) (string, string) { return "", "" }
	stateDir, output := t.TempDir(), t.TempDir()
	for _, host := range []string{"localhost:0", "localhost:65536", "bad/host:42", ":42", "localhost/api/grpc"} {
		if _, err := registerNode(ctx, host, token, stateDir, output, detect); err == nil || !strings.Contains(err.Error(), "panel host must be a bare DNS name or IP address") {
			t.Errorf("host %q: %v", host, err)
		}
	}
	if _, err := registerNode(ctx, enrollmentTestHost, "invalid", stateDir, output, detect); err == nil || !strings.Contains(err.Error(), "registration token") {
		t.Fatalf("invalid token: %v", err)
	}
	installed := t.TempDir()
	if err := os.Mkdir(filepath.Join(installed, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := registerNode(ctx, enrollmentTestHost, token, installed, output, detect); err == nil || !strings.Contains(err.Error(), "existing node directory") {
		t.Fatalf("existing node: %v", err)
	}
	for _, test := range []struct {
		name     string
		response *nodegrpc.RegisterNodeReply
		err      error
		want     string
	}{
		{"wrong node", &nodegrpc.RegisterNodeReply{NodeId: 41, Username: "valid", Password: "valid"}, nil, "invalid node enrollment data"},
		{"invalid credentials", &nodegrpc.RegisterNodeReply{NodeId: 42, Username: "bad:name", Password: "valid"}, nil, "invalid node enrollment data"},
		{"rejected token", nil, status.Error(codes.PermissionDenied, "token expired"), "token expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ca, dial := testPanel(t, enrollmentPanel{register: func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
				return test.response, test.err
			}})
			t.Setenv("YAGP_PANEL_CA_FILE", ca)
			if _, err := registerNode(ctx, enrollmentTestHost, token, stateDir, output, detect, dial); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("register error = %v, want %q", err, test.want)
			}
		})
	}
	if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
		t.Fatalf("rejected enrollment left staged files: %v, %v", entries, err)
	}
}

func TestPanelCASelection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAGP_PANEL_CA_FILE", "")
	if got, err := panelCAFile(dir); err != nil || got != "" {
		t.Fatalf("default trust = %q, %v", got, err)
	}
	ca := filepath.Join(dir, "panel-ca.pem")
	if err := os.WriteFile(ca, []byte("invalid PEM"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := panelCAFile(dir); err != nil || got != ca {
		t.Fatalf("local CA = %q, %v", got, err)
	}
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, err := registerNode(context.Background(), enrollmentTestHost, token, dir, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "load panel CA") {
		t.Fatalf("invalid local CA = %v", err)
	}
}
