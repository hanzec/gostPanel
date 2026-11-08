package nodegrpc

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sessionPanelFixture struct {
	UnimplementedV1Server
	connected chan *Session
}

type rawSessionFixture struct {
	UnimplementedV1Server
	serve func(V1_ConnectServer) error
}

func (f rawSessionFixture) Connect(stream V1_ConnectServer) error { return f.serve(stream) }

func openRawSession(t *testing.T, ctx context.Context, serve func(V1_ConnectServer) error) V1_ConnectClient {
	t.Helper()
	server := grpc.NewServer()
	RegisterV1Server(server, rawSessionFixture{serve: serve})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	stream, err := NewV1Client(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func TestSessionRejectsMalformedFramesAndHonorsRemoteTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	panel := &sessionPanelFixture{}
	dispatch := NewDispatcher(panel, V1_UpdateNodeIPs_FullMethodName)
	stream := openRawSession(t, ctx, func(stream V1_ConnectServer) error {
		return NewSession(stream, func(ctx context.Context, method string, payload []byte) ([]byte, error) {
			if method == V1_Execute_FullMethodName {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return dispatch(ctx, method, payload)
		}).Run()
	})
	for _, test := range []struct {
		frame *StreamFrame
		code  codes.Code
	}{
		{&StreamFrame{Id: 1, Method: V1_UpdateNodeIPs_FullMethodName, Payload: []byte{0x80}, TimeoutMillis: 1000}, codes.InvalidArgument},
		{&StreamFrame{Id: 2, Method: V1_Execute_FullMethodName, TimeoutMillis: 20}, codes.DeadlineExceeded},
	} {
		if err := stream.Send(test.frame); err != nil {
			t.Fatal(err)
		}
		reply, err := stream.Recv()
		if err != nil || reply.GetId() != test.frame.GetId() || codes.Code(reply.GetCode()) != test.code {
			t.Fatalf("reply for %s = %v, %v; want %s", test.frame.GetMethod(), reply, err, test.code)
		}
	}
	if err := stream.Send(&StreamFrame{}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing frame ID ended stream with %v", err)
	}
}

func TestSessionRejectsOverloadWithoutBlockingReplies(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	active := make(chan struct{}, sessionCapacity)
	connected := make(chan *Session, 1)
	stream := openRawSession(t, ctx, func(stream V1_ConnectServer) error {
		session := NewSession(stream, func(ctx context.Context, _ string, _ []byte) ([]byte, error) {
			active <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
		connected <- session
		return session.Run()
	})
	for i := range sessionCapacity + 1 {
		if err := stream.Send(&StreamFrame{Id: uint64(i + 1), Method: V1_UpdateNodeIPs_FullMethodName, TimeoutMillis: 10000}); err != nil {
			t.Fatal(err)
		}
	}
	reply, err := stream.Recv()
	if err != nil || reply.GetId() != sessionCapacity+1 || codes.Code(reply.GetCode()) != codes.ResourceExhausted {
		t.Fatalf("overload reply = %v, %v", reply, err)
	}
	for range sessionCapacity {
		select {
		case <-active:
		case <-ctx.Done():
			t.Fatal("expected calls were not dispatched")
		}
	}
	session := <-connected
	completed := make(chan error, 1)
	go func() {
		_, err := NewV1Client(session).GetObserverEndpoint(ctx, &emptypb.Empty{})
		completed <- err
	}()
	request, err := stream.Recv()
	if err != nil || request.GetMethod() != V1_GetObserverEndpoint_FullMethodName {
		t.Fatalf("reverse request while overloaded = %v, %v", request, err)
	}
	if err := stream.Send(&StreamFrame{Id: request.GetId()}); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatalf("reverse reply while overloaded: %v", err)
	}
}

func TestSessionSkipsCanceledCallWhileWaitingForWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connected := make(chan *Session, 1)
	start := make(chan struct{})
	stream := openRawSession(t, ctx, func(stream V1_ConnectServer) error {
		session := NewSession(stream, nil)
		defer session.Close()
		connected <- session
		select {
		case <-start:
			return session.Run()
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	})
	var session *Session
	select {
	case session = <-connected:
	case <-ctx.Done():
		t.Fatal("server did not accept session")
	}
	client := NewV1Client(session)
	callCtx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	completed := make(chan error, 1)
	go func() {
		_, err := client.Execute(callCtx, &CommandRequest{Path: "/canceled"})
		completed <- err
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for len(session.send) == 0 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("call never entered the send queue")
		}
	}
	cancelCall()
	if err := <-completed; status.Code(err) != codes.Canceled {
		t.Fatalf("queued call returned %v", err)
	}
	if _, err := client.Execute(callCtx, &CommandRequest{Path: "/already-canceled"}); status.Code(err) != codes.Canceled {
		t.Fatalf("already-canceled call returned %v", err)
	}
	close(start)
	go func() {
		_, err := client.Execute(ctx, &CommandRequest{Path: "/active"})
		completed <- err
	}()
	request, err := stream.Recv()
	var call CommandRequest
	if err != nil || proto.Unmarshal(request.GetPayload(), &call) != nil || call.GetPath() != "/active" {
		t.Fatalf("writer sent %v, %v", request, err)
	}
	if err := stream.Send(&StreamFrame{Id: request.GetId()}); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func (f *sessionPanelFixture) Connect(stream V1_ConnectServer) error {
	session := NewSession(stream, NewDispatcher(f, V1_UpdateNodeIPs_FullMethodName))
	f.connected <- session
	return session.Run()
}

func (*sessionPanelFixture) UpdateNodeIPs(_ context.Context, request *NodeIPsRequest) (*emptypb.Empty, error) {
	if request.GetIpv4() != "192.0.2.42" {
		return nil, status.Error(codes.InvalidArgument, "unexpected IPv4")
	}
	return &emptypb.Empty{}, nil
}

type sessionNodeFixture struct {
	UnimplementedV1Server
	started chan struct{}
	release chan struct{}
}

func (f *sessionNodeFixture) Execute(ctx context.Context, request *CommandRequest) (*CommandReply, error) {
	switch request.GetPath() {
	case "/slow":
		f.started <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "/pending":
		f.started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &CommandReply{StatusCode: 200, Body: []byte(request.GetPath())}, nil
}

func TestBidirectionalSessionCorrelatesCallsAndReleasesPendingOnDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	panel := &sessionPanelFixture{connected: make(chan *Session, 1)}
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		if method != "/api/v1/grpc/Connect" {
			return status.Errorf(codes.Unimplemented, "unexpected path %s", method)
		}
		return panel.Connect(&grpc.GenericServerStream[StreamFrame, StreamFrame]{ServerStream: stream})
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	address, err := ParsePanelAddress(listener.Addr().String() + PanelProxyPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := address.NewClientConn(grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := NewV1Client(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	node := &sessionNodeFixture{started: make(chan struct{}, 1), release: make(chan struct{})}
	nodeSession := NewSession(stream, NewDispatcher(node, V1_Execute_FullMethodName))
	go func() { _ = nodeSession.Run() }()
	defer nodeSession.Close()

	var panelSession *Session
	select {
	case panelSession = <-panel.connected:
	case <-ctx.Done():
		t.Fatalf("panel did not accept Connect: %v", ctx.Err())
	}
	defer panelSession.Close()
	panelCalls := NewV1Client(nodeSession)
	nodeCalls := NewV1Client(panelSession)
	type result struct {
		reply *CommandReply
		err   error
	}
	slow := make(chan result, 1)
	go func() {
		reply, err := nodeCalls.Execute(ctx, &CommandRequest{Path: "/slow"})
		slow <- result{reply, err}
	}()
	select {
	case <-node.started:
	case <-ctx.Done():
		t.Fatal("slow command did not reach the node")
	}

	if _, err = panelCalls.UpdateNodeIPs(ctx, &NodeIPsRequest{Ipv4: "192.0.2.42"}); err != nil {
		t.Fatalf("IP update while executing command: %v", err)
	}
	if _, err = panelCalls.UpdateNodeIPs(ctx, &NodeIPsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid IP update: %v", err)
	}
	if _, err = panelCalls.GetNftConfig(ctx, &emptypb.Empty{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("method outside allowlist: %v", err)
	}
	fast, err := nodeCalls.Execute(ctx, &CommandRequest{Path: "/fast"})
	if err != nil || string(fast.GetBody()) != "/fast" {
		t.Fatalf("fast command reply: %v, %v", fast, err)
	}
	close(node.release)
	select {
	case got := <-slow:
		if got.err != nil || string(got.reply.GetBody()) != "/slow" {
			t.Fatalf("slow command reply: %v, %v", got.reply, got.err)
		}
	case <-ctx.Done():
		t.Fatal("slow command did not finish")
	}

	// Hold a fresh command open and end the stream while its caller is waiting.
	go func() {
		reply, err := nodeCalls.Execute(ctx, &CommandRequest{Path: "/pending"})
		slow <- result{reply, err}
	}()
	select {
	case <-node.started:
	case <-ctx.Done():
		t.Fatal("pending command did not reach the node")
	}
	panelSession.Close()
	select {
	case got := <-slow:
		if status.Code(got.err) != codes.Unavailable {
			t.Fatalf("pending command after disconnect: %v", got.err)
		}
	case <-ctx.Done():
		t.Fatal("disconnect did not release pending command")
	}
}
