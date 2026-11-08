// Package streamtest opens a real gRPC node stream for tests of panel callers.
package streamtest

import (
	"context"
	"net"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type panel struct {
	nodegrpc.UnimplementedV1Server
	ready chan *nodegrpc.Session
}

func (p *panel) Connect(stream nodegrpc.V1_ConnectServer) error {
	session := nodegrpc.NewSession(stream, nil)
	p.ready <- session
	return session.Run()
}

func Open(t *testing.T, handle nodegrpc.StreamHandler) *nodegrpc.Session {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	p := &panel{ready: make(chan *nodegrpc.Session, 1)}
	nodegrpc.RegisterV1Server(server, p)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///test-panel", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	stream, err := nodegrpc.NewV1Client(conn).Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	session := nodegrpc.NewSession(stream, handle)
	t.Cleanup(session.Close)
	go func() { _ = session.Run() }()
	select {
	case panelSession := <-p.ready:
		return panelSession
	case <-time.After(5 * time.Second):
		t.Fatal("panel did not accept the test stream")
		return nil
	}
}
