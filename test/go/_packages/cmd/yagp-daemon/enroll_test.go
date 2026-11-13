package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"yagp/internal/nodebridge"
	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/proto"
)

const enrollmentTestHost = "127.0.0.1"

func testPanel(t *testing.T, register func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, int)) (string, func(context.Context, string, string) (net.Conn, error)) {
	t.Helper()
	https := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != nodegrpc.RegisterPath ||
			r.Header.Get("Content-Type") != nodegrpc.ProtobufContentType {
			t.Errorf("unexpected enrollment request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(r.Body)
		req := new(nodegrpc.RegisterNodeRequest)
		if err != nil || proto.Unmarshal(data, req) != nil {
			t.Error("enrollment request did not contain Protobuf")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply, status := register(req)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		data, err = proto.Marshal(reply)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", nodegrpc.ProtobufContentType)
		if _, err = w.Write(data); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(https.Close)
	caFile := filepath.Join(t.TempDir(), "panel.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: https.Certificate().Raw})
	if err := os.WriteFile(caFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	return caFile, func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, https.Listener.Addr().String())
	}
}

func enrolledNode() *nodegrpc.RegisterNodeReply {
	now := time.Now()
	return &nodegrpc.RegisterNodeReply{NodeId: 42, Username: "registered", Password: "test-secret",
		Credentials: &nodegrpc.NodeCredentials{
			AccessToken: "access", RefreshToken: "refresh",
			AccessExpiresAt: now.Add(15 * time.Minute).Unix(), RefreshExpiresAt: now.Add(24 * time.Hour).Unix(),
		},
	}
}

func TestRegisterNodeWritesEnrolledCredentials(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	detect := func(context.Context) (string, string) { return "192.0.2.15", "2001:db8::15" }
	ca, dial := testPanel(t, func(req *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, int) {
		if req.Token != token || req.Ipv4 != "192.0.2.15" || req.Ipv6 != "2001:db8::15" {
			t.Errorf("unexpected registration token or addresses: %s %s", req.Ipv4, req.Ipv6)
		}
		return enrolledNode(), http.StatusOK
	})
	t.Setenv("YAGP_PANEL_CA_FILE", ca)
	output := t.TempDir()
	id, err := registerNode(ctx, enrollmentTestHost, token, stateDir, output, "", detect, dial)
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
	if err != nil || len(entries) != 3 || entries[0].Name() != "gost.yaml" || entries[1].Name() != "node.yaml" || entries[2].Name() != nodebridge.PanelCredentialsFile {
		t.Fatalf("unexpected staged files: %v, %v", entries, err)
	}
	gost, err := os.ReadFile(filepath.Join(output, "gost.yaml"))
	if err != nil || !strings.Contains(string(gost), cfg.AdminAddress) || !strings.Contains(string(gost), "pathPrefix: /api") {
		t.Fatalf("invalid Gost configuration: %s, %v", gost, err)
	}
	if credentials, err := os.ReadFile(filepath.Join(output, nodebridge.PanelCredentialsFile)); err != nil ||
		!strings.Contains(string(credentials), "refresh_token: refresh") || strings.Contains(string(credentials), "test-secret") {
		t.Fatalf("public credentials file was not written separately: %v", err)
	}
	if _, err := registerNode(ctx, enrollmentTestHost, token, stateDir, output, "", detect, dial); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("re-enrollment overwrote staged files: %v", err)
	}
}

func TestRegisterNodePinsGeneratedPanelCertificate(t *testing.T) {
	var calls atomic.Int32
	caFile, dial := testPanel(t, func(req *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, int) {
		calls.Add(1)
		return enrolledNode(), http.StatusOK
	})
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
	if _, err := registerNode(t.Context(), enrollmentTestHost, baseToken+"."+strings.Repeat("0", 64), stateDir, output, "", detect, dial); err == nil ||
		!strings.Contains(err.Error(), "fingerprint mismatch") || calls.Load() != 0 {
		t.Fatalf("invalid fingerprint reached enrollment: %v, %d requests", err, calls.Load())
	}
	id, err := registerNode(t.Context(), enrollmentTestHost, baseToken+"."+fingerprint, stateDir, output, "", detect, dial)
	if err != nil || id != 42 || calls.Load() != 1 {
		t.Fatalf("enrollment with pin = %d, %v, %d requests", id, err, calls.Load())
	}
	configPath := filepath.Join(output, "node.yaml")
	config, err := readConfig(configPath)
	if err != nil || config.Host != enrollmentTestHost || config.ReportCertSHA256 != fingerprint || config.ReportCAFile != "" {
		t.Fatalf("staged certificate identity = %q, %q, %v", config.ReportCertSHA256, config.ReportCAFile, err)
	}

}

func TestReenrollPreservesExistingGostFilesAndAddresses(t *testing.T) {
	stateDir := t.TempDir()
	nodeDir := filepath.Join(stateDir, "42")
	if err := os.Mkdir(nodeDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(nodeDir, "node.yaml")
	var mismatched atomic.Bool
	ca, dial := testPanel(t, func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, int) {
		reply := enrolledNode()
		if mismatched.Load() {
			reply.Password = "other-local-secret"
		}
		return reply, http.StatusOK
	})
	t.Setenv("YAGP_PANEL_CA_FILE", "")
	files := map[string]string{
		"gost.yaml": "original local Gost configuration",
		"nft.json":  "original nftables configuration",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(nodeDir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := "node_id: 42\nusername: registered\npassword: test-secret\nadmin_address: 127.0.0.1:12345\n" +
		"observer_listen: 127.0.0.1:12346\nhost: 127.0.0.1\nreport_ca_file: " + ca + "\n" +
		"firewall:\n  enabled: true\n  policy: yagp-42\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	detect := func(context.Context) (string, string) { return "192.0.2.15", "" }
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if _, err := registerNode(t.Context(), enrollmentTestHost, "43."+secret, stateDir, "", configPath, detect, dial); err == nil ||
		!strings.Contains(err.Error(), "different node") {
		t.Fatalf("re-enrollment with another node's token: %v", err)
	}
	id, err := registerNode(t.Context(), enrollmentTestHost, "42."+secret, stateDir, "", configPath, detect, dial)
	if err != nil || id != 42 {
		t.Fatalf("re-enrollment = %d, %v", id, err)
	}
	stored, err := readConfig(configPath)
	if err != nil || stored.AdminAddress != "127.0.0.1:12345" || stored.ObserverListen != "127.0.0.1:12346" || stored.ReportCAFile != ca {
		t.Fatalf("re-enrollment did not preserve local settings: %v", err)
	}
	if stored.Firewall.Enabled == nil || !*stored.Firewall.Enabled || stored.Firewall.Policy != "yagp-42" {
		t.Fatalf("re-enrollment did not preserve firewall delegation: %+v", stored.Firewall)
	}
	for name, contents := range files {
		data, err := os.ReadFile(filepath.Join(nodeDir, name))
		if err != nil || string(data) != contents {
			t.Fatalf("re-enrollment modified %s: %v", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(nodeDir, nodebridge.PanelCredentialsFile)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("re-enrollment did not write private credentials: %v, %v", info, err)
	}
	if _, err = registerNode(t.Context(), enrollmentTestHost, "42."+secret, stateDir, "", configPath+".missing", detect, dial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("re-enrollment without its configuration: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	mismatched.Store(true)
	if _, err = registerNode(t.Context(), enrollmentTestHost, "42."+secret, stateDir, "", configPath, detect, dial); err == nil ||
		!strings.Contains(err.Error(), "local Gost credentials do not match") {
		t.Fatalf("re-enrollment with different Gost credentials: %v", err)
	}
	if after, err := os.ReadFile(configPath); err != nil || string(after) != string(before) {
		t.Fatalf("re-enrollment replaced local configuration despite the mismatch: %v", err)
	}
}

func TestRegisterNodeRejectsInvalidInputAndPanelReply(t *testing.T) {
	ctx := context.Background()
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	detect := func(context.Context) (string, string) { return "", "" }
	stateDir, output := t.TempDir(), t.TempDir()
	for _, host := range []string{"localhost:0", "localhost:65536", "bad/host:42", ":42", "localhost/api/grpc"} {
		if _, err := registerNode(ctx, host, token, stateDir, output, "", detect); err == nil || !strings.Contains(err.Error(), "panel host must be a bare DNS name or IP address") {
			t.Errorf("host %q: %v", host, err)
		}
	}
	if _, err := registerNode(ctx, enrollmentTestHost, "invalid", stateDir, output, "", detect); err == nil || !strings.Contains(err.Error(), "registration token") {
		t.Fatalf("invalid token: %v", err)
	}
	installed := t.TempDir()
	if err := os.Mkdir(filepath.Join(installed, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := registerNode(ctx, enrollmentTestHost, token, installed, output, "", detect); err == nil || !strings.Contains(err.Error(), "existing node files") {
		t.Fatalf("existing node: %v", err)
	}
	flat := t.TempDir()
	config := filepath.Join(flat, "node.yaml")
	if err := os.WriteFile(config, []byte("node_id: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := registerNode(ctx, enrollmentTestHost, token, flat, output, "", detect); err == nil || !strings.Contains(err.Error(), config) {
		t.Fatalf("existing flat node: %v", err)
	}
	for _, test := range []struct {
		name     string
		response *nodegrpc.RegisterNodeReply
		status   int
		want     string
	}{
		{"wrong node", &nodegrpc.RegisterNodeReply{NodeId: 41, Username: "valid", Password: "valid"}, http.StatusOK, "invalid node enrollment data"},
		{"invalid credentials", &nodegrpc.RegisterNodeReply{NodeId: 42, Username: "bad:name", Password: "valid"}, http.StatusOK, "invalid node enrollment data"},
		{"invalid public credentials", &nodegrpc.RegisterNodeReply{NodeId: 42, Username: "valid", Password: "valid"}, http.StatusOK, "invalid credentials"},
		{"rejected token", nil, http.StatusUnauthorized, "panel returned HTTP 401"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ca, dial := testPanel(t, func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, int) {
				return test.response, test.status
			})
			t.Setenv("YAGP_PANEL_CA_FILE", ca)
			if _, err := registerNode(ctx, enrollmentTestHost, token, stateDir, output, "", detect, dial); err == nil || !strings.Contains(err.Error(), test.want) {
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
	if _, err := registerNode(context.Background(), enrollmentTestHost, token, dir, t.TempDir(), "", nil); err == nil || !strings.Contains(err.Error(), "load panel CA") {
		t.Fatalf("invalid local CA = %v", err)
	}
}

func TestRegisterNodeReportsFailedIPDetection(t *testing.T) {
	ca, dial := testPanel(t, func(*nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, int) {
		return nil, http.StatusBadRequest
	})
	t.Setenv("YAGP_PANEL_CA_FILE", ca)
	token := "42." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, ipv4 := range []string{"", "192.0.2.15"} {
		detect := func(context.Context) (string, string) { return ipv4, "" }
		_, err := registerNode(t.Context(), enrollmentTestHost, token, t.TempDir(), t.TempDir(), "", detect, dial)
		var response *nodegrpc.HTTPStatusError
		if !errors.As(err, &response) || response.StatusCode != http.StatusBadRequest {
			t.Fatalf("register error = %v, want HTTP 400", err)
		}
		if strings.Contains(err.Error(), "could not detect a node IP address") != (ipv4 == "") {
			t.Fatalf("detected IPv4 %q, register error = %v", ipv4, err)
		}
	}
}
