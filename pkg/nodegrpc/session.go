package nodegrpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	NodeIDMetadata = "x-yagp-node-id"

	// MaxStreamFrameBytes includes the protobuf envelope around a 4 MiB Gost body.
	MaxStreamFrameBytes = 5 << 20

	sessionCapacity = 32
	sessionTimeout  = 30 * time.Second
)

type Stream interface {
	Context() context.Context
	Send(*StreamFrame) error
	Recv() (*StreamFrame, error)
}

type StreamHandler func(context.Context, string, []byte) ([]byte, error)

// Session adapts the bidirectional stream to the generated unary client interface.
type Session struct {
	stream Stream
	handle StreamHandler
	ctx    context.Context
	cancel context.CancelFunc
	send   chan outboundFrame
	active chan struct{}

	nextID  atomic.Uint64
	mu      sync.Mutex
	pending map[uint64]chan *StreamFrame
}

type outboundFrame struct {
	frame  *StreamFrame
	caller context.Context
}

func NewSession(stream Stream, handler StreamHandler) *Session {
	ctx, cancel := context.WithCancel(stream.Context())
	if handler == nil {
		handler = NewDispatcher(UnimplementedV1Server{})
	}
	return &Session{
		stream: stream, handle: handler, ctx: ctx, cancel: cancel,
		send: make(chan outboundFrame, sessionCapacity), active: make(chan struct{}, sessionCapacity),
		pending: make(map[uint64]chan *StreamFrame),
	}
}

func (s *Session) Done() <-chan struct{} { return s.ctx.Done() }
func (s *Session) Close()                { s.cancel() }

// Run owns the stream's single reader and single writer. Returning from the
// server handler or canceling the client stream releases a blocked Send or Recv.
func (s *Session) Run() error {
	defer s.Close()
	errs := make(chan error, 2)
	go func() { errs <- s.readFrames() }()
	go func() { errs <- s.writeFrames() }()
	select {
	case err := <-errs:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	case <-s.Done():
		return status.FromContextError(s.ctx.Err()).Err()
	}
}

func (s *Session) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	request := args.(proto.Message)
	if proto.Size(request) > MaxStreamFrameBytes {
		return status.Error(codes.ResourceExhausted, "RPC request exceeds stream message limit")
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		return status.Error(codes.Internal, "cannot encode RPC request")
	}
	frame := &StreamFrame{Id: s.nextID.Add(1), Method: method, Payload: payload, TimeoutMillis: sessionTimeout.Milliseconds()}
	if proto.Size(frame) > MaxStreamFrameBytes {
		return status.Error(codes.ResourceExhausted, "RPC request exceeds stream message limit")
	}
	result := make(chan *StreamFrame, 1)
	s.mu.Lock()
	s.pending[frame.Id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, frame.Id)
		s.mu.Unlock()
	}()

	select {
	case s.send <- outboundFrame{frame: frame, caller: ctx}:
	case <-s.Done():
		return status.Error(codes.Unavailable, "node stream disconnected")
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
	select {
	case response := <-result:
		if response.GetCode() != 0 {
			return status.Error(codes.Code(response.GetCode()), response.GetError())
		}
		if err := proto.Unmarshal(response.GetPayload(), reply.(proto.Message)); err != nil {
			return status.Error(codes.Internal, "cannot decode RPC response")
		}
		return nil
	case <-s.Done():
		return status.Error(codes.Unavailable, "node stream disconnected")
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func (*Session) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "nested streams are not supported")
}

func (s *Session) readFrames() error {
	for {
		frame, err := s.stream.Recv()
		if err != nil {
			return err
		}
		if frame.GetId() == 0 {
			return status.Error(codes.InvalidArgument, "missing RPC frame ID")
		}
		if frame.GetMethod() == "" {
			s.deliver(frame)
			continue
		}
		select {
		case s.active <- struct{}{}:
			go s.dispatch(frame)
		default:
			// Keep the reader free to accept replies for our own pending calls.
			response := &StreamFrame{Id: frame.GetId(), Code: int32(codes.ResourceExhausted), Error: "too many pending RPCs"}
			select {
			case s.send <- outboundFrame{frame: response}:
			default:
				return status.Error(codes.ResourceExhausted, "node stream send queue is full")
			}
		}
	}
}

func (s *Session) deliver(frame *StreamFrame) {
	s.mu.Lock()
	result := s.pending[frame.GetId()]
	delete(s.pending, frame.GetId())
	s.mu.Unlock()
	if result != nil {
		result <- frame
	}
}

func (s *Session) dispatch(frame *StreamFrame) {
	defer func() { <-s.active }()
	timeout := time.Duration(min(max(frame.GetTimeoutMillis(), 1), sessionTimeout.Milliseconds())) * time.Millisecond
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	payload, err := s.handle(ctx, frame.GetMethod(), frame.GetPayload())
	response := &StreamFrame{Id: frame.GetId(), Payload: payload}
	if err != nil {
		result := status.Convert(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result = status.FromContextError(err)
		}
		response.Code, response.Error = int32(result.Code()), result.Message()
	}
	if proto.Size(response) > MaxStreamFrameBytes {
		response = &StreamFrame{Id: frame.GetId(), Code: int32(codes.ResourceExhausted), Error: "RPC response exceeds stream message limit"}
	}
	select {
	case s.send <- outboundFrame{frame: response}:
	case <-s.Done():
	}
}

func (s *Session) writeFrames() error {
	for {
		select {
		case message := <-s.send:
			if message.caller != nil {
				// A command can expire while waiting for the transport's send window.
				if message.caller.Err() != nil {
					continue
				}
				if deadline, ok := message.caller.Deadline(); ok {
					message.frame.TimeoutMillis = max(1, min(message.frame.TimeoutMillis, time.Until(deadline).Milliseconds()))
				}
			}
			if err := s.stream.Send(message.frame); err != nil {
				return err
			}
		case <-s.Done():
			return s.ctx.Err()
		}
	}
}

// NewDispatcher reuses the generated request decoders for the selected methods.
func NewDispatcher(server V1Server, allowed ...string) StreamHandler {
	methods := make(map[string]grpc.MethodHandler, len(allowed))
	for _, desc := range V1_ServiceDesc.Methods {
		fullName := "/" + V1_ServiceDesc.ServiceName + "/" + desc.MethodName
		for _, name := range allowed {
			if fullName == name {
				methods[name] = desc.Handler
				break
			}
		}
	}
	return func(ctx context.Context, method string, payload []byte) ([]byte, error) {
		handler := methods[method]
		if handler == nil {
			return nil, status.Error(codes.Unimplemented, "RPC is unavailable on this side of the stream")
		}
		reply, err := handler(server, ctx, func(value any) error {
			if err := proto.Unmarshal(payload, value.(proto.Message)); err != nil {
				return status.Error(codes.InvalidArgument, "invalid RPC request payload")
			}
			return nil
		}, nil)
		if err != nil {
			return nil, err
		}
		response := reply.(proto.Message)
		if proto.Size(response) > MaxStreamFrameBytes {
			return nil, status.Error(codes.ResourceExhausted, "RPC response exceeds stream message limit")
		}
		encoded, err := proto.Marshal(response)
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot encode RPC response")
		}
		return encoded, nil
	}
}
