package nodegrpc

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

func TestParsePanelHost(t *testing.T) {
	for _, host := range []string{
		"", "https://panel.example.test", "panel.example.test:443", "panel.example.test/api/grpc",
		"panel.example.test/api/v1/grpc", "panel.example.test?query", "admin@panel.example.test",
		"[2001:db8::2]", "fe80::1%eth0", "panel..example.test", "-panel.example.test",
		"panel_.example.test", "面板.example.test", strings.Repeat("a", 64) + ".test", strings.Repeat("a", 254),
	} {
		if _, err := ParsePanelHost(host); err == nil {
			t.Errorf("invalid panel host %q was accepted", host)
		}
	}
	for host, want := range map[string]string{
		"PANEL.example.test.": "panel.example.test", "localhost": "localhost",
		"127.0.0.1": "127.0.0.1", "2001:0db8::2": "2001:db8::2",
	} {
		panel, err := ParsePanelHost(host)
		if err != nil || panel.Host() != want {
			t.Fatalf("panel host for %q = %q, %v", host, panel.Host(), err)
		}
	}
}

func TestPanelClientSendsProtobufToFixedHTTPSPath(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != RegisterPath || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != ProtobufContentType {
			t.Errorf("unexpected request: %s %q %q", r.Method, r.URL, r.Header.Get("Content-Type"))
			http.NotFound(w, r)
			return
		}
		data, err := io.ReadAll(r.Body)
		var request RegisterNodeRequest
		if err != nil || proto.Unmarshal(data, &request) != nil || request.GetToken() != "fixture" ||
			request.GetIpv4() != "203.0.113.42" || request.GetIpv6() != "2001:db8::42" {
			t.Errorf("invalid Protobuf request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response, _ := proto.Marshal(&RegisterNodeReply{
			NodeId: 42, Username: "node-42", Password: "local-gost-password",
			Credentials: &NodeCredentials{AccessToken: "issued-access", RefreshToken: "issued-refresh", AccessExpiresAt: 100, RefreshExpiresAt: 200},
		})
		w.Header().Set("Content-Type", ProtobufContentType)
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	panel, err := ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "panel-ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, ca, 0600); err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := panel.TLSConfig(caFile, "")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext: func(ctx context.Context, _, target string) (net.Conn, error) {
			if target != "127.0.0.1:443" {
				t.Errorf("dial target = %q, want 127.0.0.1:443", target)
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var response RegisterNodeReply
	err = PostProtobuf(ctx, &http.Client{Transport: transport}, panel.HTTPURL(RegisterPath), &RegisterNodeRequest{
		Token: "fixture", Ipv4: "203.0.113.42", Ipv6: "2001:db8::42",
	}, &response)
	if err != nil || response.GetNodeId() != 42 || response.GetUsername() != "node-42" || response.GetPassword() != "local-gost-password" {
		t.Fatalf("registration via fixed TLS endpoint = %v, %v", &response, err)
	}
	credentials := response.GetCredentials()
	if credentials.GetAccessToken() != "issued-access" || credentials.GetRefreshToken() != "issued-refresh" ||
		credentials.GetAccessExpiresAt() != 100 || credentials.GetRefreshExpiresAt() != 200 {
		t.Fatal("registration did not return the expected credential pair")
	}
}
