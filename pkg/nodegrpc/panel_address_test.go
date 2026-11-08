package nodegrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestParsePanelAddress(t *testing.T) {
	for _, address := range []string{
		"panel.example.test", "https://panel.example.test:443/api/v1/grpc", "panel.example.test:0",
		"panel.example.test:65536", "panel.example.test:443/api/v1", "panel.example.test:443/api/v1/grpc/",
		"panel.example.test:443/api/v1/grpc?query", "foo/bar:443/api/v1/grpc", "admin@panel.example.test:443",
	} {
		if _, err := ParsePanelAddress(address); err == nil {
			t.Errorf("invalid panel address %q was accepted", address)
		}
	}
	if address, err := ParsePanelAddress("[2001:db8::2]:443/api/v1/grpc"); err != nil || !address.Proxied() {
		t.Fatalf("IPv6 proxy address = %+v, %v", address, err)
	}
}

type panelRouteFixture struct{ UnimplementedV1Server }

func (panelRouteFixture) RegisterNode(context.Context, *RegisterNodeRequest) (*RegisterNodeReply, error) {
	return &RegisterNodeReply{NodeId: 42}, nil
}

func TestPanelClientSendsExpectedRPCPath(t *testing.T) {
	for _, tt := range []struct{ name, prefix, requestPath string }{
		{"direct TLS", "", "/yagp.v1/RegisterNode"},
		{"proxy HTTPS", "/api/v1/grpc", "/api/v1/grpc/RegisterNode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rpcServer := grpc.NewServer()
			defer rpcServer.Stop()
			RegisterV1Server(rpcServer, panelRouteFixture{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 || r.URL.Path != tt.requestPath {
					t.Errorf("received %s request for %q, want HTTP/2 %s", r.Proto, r.URL.Path, tt.requestPath)
					http.NotFound(w, r)
					return
				}
				if tt.prefix != "" {
					r.URL.Path = "/yagp.v1/RegisterNode"
				}
				rpcServer.ServeHTTP(w, r)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()

			address, err := ParsePanelAddress(server.Listener.Addr().String() + tt.prefix)
			if err != nil || address.Proxied() != (tt.prefix != "") {
				t.Fatalf("panel address = %+v, %v", address, err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			conn, err := address.NewClientConn(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots})))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			response, err := NewV1Client(conn).RegisterNode(ctx, &RegisterNodeRequest{})
			if err != nil || response.GetNodeId() != 42 {
				t.Fatalf("registration via %s = %v, %v", tt.name, response, err)
			}
		})
	}
}
