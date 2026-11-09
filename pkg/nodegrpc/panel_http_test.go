package nodegrpc

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPostProtobufRejectsInvalidResponses(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/untrusted", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/untrusted", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("credential request followed a redirect")
		w.WriteHeader(http.StatusBadRequest)
	})
	mux.HandleFunc("/wrong-type", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/too-large", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ProtobufContentType)
		_, _ = w.Write(bytes.Repeat([]byte{0x80}, MaxStreamFrameBytes+1))
	})
	mux.HandleFunc("/malformed", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ProtobufContentType)
		_, _ = w.Write([]byte{0x80})
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	for _, test := range []struct{ path, message string }{
		{"/redirect", "HTTP 307"},
		{"/wrong-type", "invalid Protobuf content type"},
		{"/too-large", "exceeds message limit"},
		{"/malformed", "cannot parse invalid wire-format data"},
	} {
		t.Run(test.path, func(t *testing.T) {
			err := PostProtobuf(t.Context(), server.Client(), server.URL+test.path, &RefreshNodeRequest{RefreshToken: "private"}, &NodeCredentials{})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("POST returned %v, want %q", err, test.message)
			}
		})
	}
}

func TestWebSocketRequiresNodeProtocol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+SessionPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer deny" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			defer conn.CloseNow()
			_, _, _ = conn.Read(r.Context())
		}
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	host, _ := ParsePanelHost("127.0.0.1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := host.DialSession(ctx, client, 42, "deny")
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized session returned %v", err)
	}
	_, err = host.DialSession(ctx, client, 42, "allow")
	if err == nil || !strings.Contains(err.Error(), "negotiate the node protocol") {
		t.Fatalf("missing subprotocol returned %v", err)
	}
}

func TestWebSocketRejectsNonProtobufFrames(t *testing.T) {
	for _, test := range []struct {
		kind    websocket.MessageType
		payload []byte
		message string
	}{
		{websocket.MessageText, []byte("{}"), "expected a binary Protobuf message"},
		{websocket.MessageBinary, []byte{0x80}, "cannot parse invalid wire-format data"},
	} {
		t.Run(test.message, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			stream := openRawSession(t, ctx, func(stream *WebSocketStream) error {
				return stream.conn.Write(stream.ctx, test.kind, test.payload)
			})
			if _, err := stream.Recv(); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("received malformed message: %v", err)
			}
		})
	}
}
