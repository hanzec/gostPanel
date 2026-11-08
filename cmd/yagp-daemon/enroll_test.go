package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagp/internal/nodebridge"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

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

func (p enrollmentPanel) UpdateNodeIPs(context.Context, *nodegrpc.NodeIPsRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func testPanel(t *testing.T, panel enrollmentPanel) (address, caFile string) {
	t.Helper()
	https := httptest.NewTLSServer(nil)
	cert := https.TLS.Certificates[0]
	https.Close()
	caFile = filepath.Join(t.TempDir(), "panel.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	if err := os.WriteFile(caFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})))
	nodegrpc.RegisterV1Server(server, panel)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), caFile
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
	address, ca := testPanel(t, panel)
	t.Setenv("YAGP_PANEL_CA_FILE", ca)
	output := t.TempDir()
	id, err := registerNode(ctx, address, token, stateDir, output, detect)
	if err != nil || id != 42 {
		t.Fatalf("enrollment = %d, %v", id, err)
	}
	configPath := filepath.Join(output, "node.yaml")
	cfg, err := readConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != id || cfg.Username != "registered" || cfg.Password != "test-secret" ||
		cfg.ReportAddress != address || cfg.ReportCAFile != ca || cfg.ObserverListen == cfg.AdminAddress ||
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
	if err := run(ctx, []string{"nft-template", "-c", configPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerNode(ctx, address, token, stateDir, output, detect); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("re-enrollment overwrote staged files: %v", err)
	}
}

func TestRegisterNodeRejectsInvalidInputAndPanelReply(t *testing.T) {
	ctx := context.Background()
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	detect := func(context.Context) (string, string) { return "", "" }
	stateDir, output := t.TempDir(), t.TempDir()
	for _, address := range []string{"localhost", "localhost:0", "localhost:65536", "bad/host:42", ":42"} {
		if _, err := registerNode(ctx, address, token, stateDir, output, detect); err == nil || !strings.Contains(err.Error(), "HOST:PORT") {
			t.Errorf("address %q: %v", address, err)
		}
	}
	if _, err := registerNode(ctx, "127.0.0.1:42", "invalid", stateDir, output, detect); err == nil || !strings.Contains(err.Error(), "registration token") {
		t.Fatalf("invalid token: %v", err)
	}
	installed := t.TempDir()
	if err := os.Mkdir(filepath.Join(installed, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := registerNode(ctx, "127.0.0.1:42", token, installed, output, detect); err == nil || !strings.Contains(err.Error(), "existing node directory") {
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
			address, ca := testPanel(t, enrollmentPanel{register: func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
				return test.response, test.err
			}})
			t.Setenv("YAGP_PANEL_CA_FILE", ca)
			if _, err := registerNode(ctx, address, token, stateDir, output, detect); err == nil || !strings.Contains(err.Error(), test.want) {
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
	if _, err := registerNode(context.Background(), "127.0.0.1:42", token, dir, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "load panel CA") {
		t.Fatalf("invalid local CA = %v", err)
	}
}
