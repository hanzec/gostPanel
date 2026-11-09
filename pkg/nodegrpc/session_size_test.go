package nodegrpc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type commandSizeFixture struct {
	UnimplementedV1Server
	body []byte
}

func TestSessionRejectsOversizedWireFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream := openRawSession(t, ctx, func(stream *WebSocketStream) error {
		return NewSession(stream, nil).Run()
	})
	if err := stream.Send(&StreamFrame{Id: 1, Payload: make([]byte, MaxStreamFrameBytes)}); err != nil && !errors.Is(err, io.EOF) && websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("Send() = %v", err)
	}
	if _, err := stream.Recv(); websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("Recv() = %v, want WebSocket message too big", err)
	}
}

func (f commandSizeFixture) Execute(_ context.Context, request *CommandRequest) (*CommandReply, error) {
	switch request.GetPath() {
	case "/large-reply-payload":
		return &CommandReply{Body: f.body}, nil
	case "/large-reply-frame":
		// The bytes field adds a one-byte tag and a four-byte length.
		return &CommandReply{Body: f.body[:MaxStreamFrameBytes-5]}, nil
	default:
		return &CommandReply{StatusCode: 200}, nil
	}
}

func TestSessionRejectsOversizedCallsWithoutDisconnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body := make([]byte, MaxStreamFrameBytes+1)
	stream := openRawSession(t, ctx, func(stream *WebSocketStream) error {
		dispatcher := NewDispatcher(commandSizeFixture{body: body}, V1_Execute_FullMethodName)
		return NewSession(stream, dispatcher).Run()
	})
	session := NewSession(stream, nil)
	go func() { _ = session.Run() }()
	defer session.Close()
	client := NewV1Client(session)

	cases := []struct {
		name    string
		request *CommandRequest
		message string
	}{
		{"request payload", &CommandRequest{Body: body}, "RPC request exceeds stream message limit"},
		{"request envelope", &CommandRequest{Body: body[:MaxStreamFrameBytes-5]}, "RPC request exceeds stream message limit"},
		{"response payload", &CommandRequest{Path: "/large-reply-payload"}, "RPC response exceeds stream message limit"},
		{"response envelope", &CommandRequest{Path: "/large-reply-frame"}, "RPC response exceeds stream message limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Execute(ctx, tc.request)
			got := status.Convert(err)
			if got.Code() != codes.ResourceExhausted || got.Message() != tc.message {
				t.Fatalf("Execute() = %v, want ResourceExhausted: %s", err, tc.message)
			}
		})
	}
	if reply, err := client.Execute(ctx, &CommandRequest{}); err != nil || reply.GetStatusCode() != 200 {
		t.Fatalf("Execute() on the same stream = (%v, %v), want status 200", reply, err)
	}
}
