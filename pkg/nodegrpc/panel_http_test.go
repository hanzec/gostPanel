package nodegrpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

func TestPanelRefreshSendsCredentialsInProtobufBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RefreshPath || r.URL.RawQuery != "" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected refresh request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payload, err := io.ReadAll(r.Body)
		var request RefreshNodeRequest
		if err != nil || proto.Unmarshal(payload, &request) != nil || request.GetNodeId() != 42 ||
			request.GetRefreshToken() != "prior-secret" || request.GetRequestId() != "same-request" {
			t.Error("refresh request did not preserve the node, credential, and retry ID")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := proto.Marshal(&NodeCredentials{AccessToken: "next-access", RefreshToken: "next-refresh"})
		w.Header().Set("Content-Type", ProtobufContentType)
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	var reply NodeCredentials
	if err := PostProtobuf(t.Context(), server.Client(), server.URL+RefreshPath, &RefreshNodeRequest{
		NodeId: 42, RefreshToken: "prior-secret", RequestId: "same-request",
	}, &reply); err != nil || reply.GetAccessToken() != "next-access" || reply.GetRefreshToken() != "next-refresh" {
		t.Fatalf("refresh response was not decoded: %v", err)
	}
}

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

func TestPanelHTTPStatusIdentifiesRequestWithoutCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "TencentEdgeOne")
		w.Header().Set("EO-LOG-UUID", "edge-request-42")
		http.Error(w, "private response body", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	endpoint := server.URL + RefreshPath
	err := PostProtobuf(t.Context(), server.Client(), endpoint+"?secret=private-query", &RefreshNodeRequest{RefreshToken: "private-refresh"}, &NodeCredentials{})
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusNotFound {
		t.Fatalf("refresh returned %v", err)
	}
	want := `panel returned HTTP 404 for POST ` + endpoint + ` Server="TencentEdgeOne" EO-LOG-UUID="edge-request-42"`
	if err.Error() != want {
		t.Fatalf("refresh diagnostic = %q, want %q", err, want)
	}
}

func TestWebSocketRequiresNodeProtocol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+SessionPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer missing-route" {
			w.Header().Set("EO-LOG-UUID", "session-request-42")
			w.WriteHeader(http.StatusNotFound)
			return
		}
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
	_, err = host.DialSession(ctx, client, 42, "missing-route")
	want := `panel returned HTTP 404 for GET ` + host.HTTPURL(SessionPath) + ` EO-LOG-UUID="session-request-42"`
	if err == nil || err.Error() != want {
		t.Fatalf("missing WebSocket route returned %v, want %q", err, want)
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

func TestWebSocketHeartbeatRequiresPong(t *testing.T) {
	for _, test := range []struct {
		name         string
		respond      bool
		cancelParent bool
	}{
		{"idle peer responds until stream closes", true, false},
		{"idle peer responds until context is canceled", true, true},
		{"unresponsive peer is disconnected", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pings := make(chan struct{}, 2)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
					Subprotocols: []string{webSocketProtocol},
					OnPingReceived: func(context.Context, []byte) bool {
						select {
						case pings <- struct{}{}:
						default:
						}
						return test.respond
					},
				})
				if err == nil {
					defer conn.CloseNow()
					_, _, _ = conn.Read(r.Context())
				}
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{
				HTTPClient: server.Client(), Subprotocols: []string{webSocketProtocol},
			})
			if err != nil {
				t.Fatal(err)
			}
			streamCtx, stop := context.WithCancel(ctx)
			defer stop()
			stream := newWebSocketStream(streamCtx, conn)
			defer stream.Close()
			session := NewSession(stream, nil)
			sessionDone := make(chan error, 1)
			go func() { sessionDone <- session.Run() }()
			heartbeatDone := make(chan struct{})
			go func() {
				stream.heartbeat(10*time.Millisecond, 200*time.Millisecond)
				close(heartbeatDone)
			}()
			select {
			case <-pings:
			case <-ctx.Done():
				t.Fatal("idle node did not ping the panel")
			}
			if test.respond {
				select {
				case <-pings:
				case <-stream.Context().Done():
					t.Fatal("idle connection did not survive the first heartbeat")
				}
				if test.cancelParent {
					stop()
				} else {
					_ = stream.Close()
				}
			} else {
				select {
				case <-stream.Context().Done():
					if ctx.Err() != nil {
						t.Fatal("connection stayed open without a pong")
					}
				case <-ctx.Done():
					t.Fatal("timed out waiting for the unresponsive socket to close")
				}
			}
			select {
			case <-heartbeatDone:
			case <-ctx.Done():
				t.Fatal("heartbeat did not exit after the stream ended")
			}
			select {
			case <-sessionDone:
			case <-ctx.Done():
				t.Fatal("node session did not end, preventing automatic reconnect")
			}
		})
	}
}

func TestDialSessionPingsBeforeDefaultEdgeIdleTimeout(t *testing.T) {
	ping := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{webSocketProtocol},
			OnPingReceived: func(context.Context, []byte) bool {
				select {
				case ping <- struct{}{}:
				default:
				}
				return true
			},
		})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	host, _ := ParsePanelHost("127.0.0.1")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stream, err := host.DialSession(ctx, client, 42, "access-token")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	go func() { _, _ = stream.Recv() }()
	select {
	case <-ping:
	case <-ctx.Done():
		t.Fatal("production session did not send a native WebSocket ping before EdgeOne's default idle timeout")
	}
}
