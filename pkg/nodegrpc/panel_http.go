package nodegrpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

const (
	RegisterPath        = "/api/node/v2/register"
	RefreshPath         = "/api/node/v2/refresh"
	SessionPath         = "/api/node/v2/session"
	ProtobufContentType = "application/x-protobuf"
	webSocketProtocol   = "yagp.node.v2"
	webSocketHeartbeat  = 30 * time.Second
)

type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("panel returned HTTP %d", e.StatusCode)
}

func (h PanelHost) HTTPURL(path string) string {
	return (&url.URL{Scheme: "https", Host: net.JoinHostPort(h.host, PanelPort), Path: path}).String()
}

func PostProtobuf(ctx context.Context, client *http.Client, endpoint string, request, response proto.Message) error {
	payload, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ProtobufContentType)
	req.Header.Set("Accept", ProtobufContentType)
	result, err := panelHTTPClient(client).Do(req)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		return &HTTPStatusError{StatusCode: result.StatusCode}
	}
	if result.Header.Get("Content-Type") != ProtobufContentType {
		return errors.New("panel returned an invalid Protobuf content type")
	}
	payload, err = io.ReadAll(io.LimitReader(result.Body, MaxStreamFrameBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > MaxStreamFrameBytes {
		return errors.New("panel response exceeds message limit")
	}
	return proto.Unmarshal(payload, response)
}

func panelHTTPClient(client *http.Client) *http.Client {
	copy := *client
	// A redirected request could deliver enrollment or refresh secrets to another host.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func (h PanelHost) DialSession(ctx context.Context, client *http.Client, nodeID uint64, accessToken string) (*WebSocketStream, error) {
	handshake, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(handshake, h.HTTPURL(SessionPath), &websocket.DialOptions{
		HTTPClient:   panelHTTPClient(client),
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + accessToken}, http.CanonicalHeaderKey(NodeIDMetadata): {strconv.FormatUint(nodeID, 10)}},
		Subprotocols: []string{webSocketProtocol},
	})
	if err != nil {
		if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
			return nil, &HTTPStatusError{StatusCode: response.StatusCode}
		}
		return nil, err
	}
	if conn.Subprotocol() != webSocketProtocol {
		_ = conn.CloseNow()
		return nil, errors.New("panel did not negotiate the node protocol")
	}
	stream := newWebSocketStream(ctx, conn)
	go stream.heartbeat()
	return stream, nil
}

func AcceptSession(w http.ResponseWriter, r *http.Request) (*WebSocketStream, error) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{webSocketProtocol}})
	if err != nil {
		return nil, err
	}
	if conn.Subprotocol() != webSocketProtocol {
		_ = conn.Close(websocket.StatusProtocolError, "node protocol required")
		return nil, errors.New("node did not negotiate the session protocol")
	}
	return newWebSocketStream(r.Context(), conn), nil
}

type WebSocketStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   *websocket.Conn
}

func newWebSocketStream(ctx context.Context, conn *websocket.Conn) *WebSocketStream {
	ctx, cancel := context.WithCancel(ctx)
	conn.SetReadLimit(MaxStreamFrameBytes)
	return &WebSocketStream{ctx: ctx, cancel: cancel, conn: conn}
}

func (s *WebSocketStream) Context() context.Context { return s.ctx }

func (s *WebSocketStream) Close() error {
	s.cancel()
	return s.conn.CloseNow()
}

func (s *WebSocketStream) Send(frame *StreamFrame) error {
	payload, err := proto.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, sessionTimeout)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageBinary, payload)
}

func (s *WebSocketStream) Recv() (*StreamFrame, error) {
	kind, payload, err := s.conn.Read(s.ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary {
		return nil, errors.New("expected a binary Protobuf message")
	}
	var frame StreamFrame
	if err := proto.Unmarshal(payload, &frame); err != nil {
		return nil, err
	}
	return &frame, nil
}

func (s *WebSocketStream) heartbeat() {
	ticker := time.NewTicker(webSocketHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			err := s.conn.Ping(ctx)
			cancel()
			if err != nil {
				_ = s.Close()
				return
			}
		}
	}
}
