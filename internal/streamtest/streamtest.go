// Package streamtest opens a real WebSocket node session for tests of panel callers.
package streamtest

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"
)

func Open(t *testing.T, handle nodegrpc.StreamHandler) *nodegrpc.Session {
	t.Helper()
	ready := make(chan *nodegrpc.Session, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+nodegrpc.SessionPath, func(w http.ResponseWriter, r *http.Request) {
		stream, err := nodegrpc.AcceptSession(w, r)
		if err != nil {
			return
		}
		defer stream.Close()
		session := nodegrpc.NewSession(stream, nil)
		ready <- session
		_ = session.Run()
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	host, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	stream, err := host.DialSession(t.Context(), client, 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	session := nodegrpc.NewSession(stream, handle)
	t.Cleanup(session.Close)
	go func() { _ = session.Run() }()
	select {
	case panelSession := <-ready:
		return panelSession
	case <-time.After(5 * time.Second):
		t.Fatal("panel did not accept the test stream")
		return nil
	}
}
