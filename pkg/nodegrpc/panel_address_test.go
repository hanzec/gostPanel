package nodegrpc

import (
	"context"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
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

type panelRouteFixture struct{ UnimplementedV1Server }

func (panelRouteFixture) RegisterNode(context.Context, *RegisterNodeRequest) (*RegisterNodeReply, error) {
	return &RegisterNodeReply{NodeId: 42}, nil
}

func TestPanelClientSendsExpectedRPCPath(t *testing.T) {
	rpcServer := grpc.NewServer()
	defer rpcServer.Stop()
	RegisterPanelServer(rpcServer, panelRouteFixture{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 || r.URL.Path != "/api/grpc/RegisterNode" {
			t.Errorf("unexpected %s request for %q", r.Proto, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		rpcServer.ServeHTTP(w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	panel, err := ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "panel-ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, ca, 0600); err != nil {
		t.Fatal(err)
	}
	transport, err := panel.ClientTLS(caFile, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := panel.NewClientConn(grpc.WithTransportCredentials(transport), grpc.WithContextDialer(
		func(ctx context.Context, target string) (net.Conn, error) {
			if target != "127.0.0.1:443" {
				t.Errorf("dial target = %q, want 127.0.0.1:443", target)
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	response, err := NewV1Client(conn).RegisterNode(ctx, &RegisterNodeRequest{})
	if err != nil || response.GetNodeId() != 42 {
		t.Fatalf("registration via fixed TLS endpoint = %v, %v", response, err)
	}
}
