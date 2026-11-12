package streamtest

import (
	"context"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"
)

type echoNode struct{ nodegrpc.UnimplementedV1Server }

func (echoNode) Execute(_ context.Context, request *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	return &nodegrpc.CommandReply{StatusCode: 200, Body: request.GetBody()}, nil
}

func TestOpenCarriesCommandsAndClosesWithTest(t *testing.T) {
	var session *nodegrpc.Session
	if !t.Run("connected", func(t *testing.T) {
		session = Open(t, nodegrpc.NewDispatcher(echoNode{}, nodegrpc.V1_Execute_FullMethodName))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		client := nodegrpc.NewV1Client(session)
		reply, err := client.Execute(ctx, &nodegrpc.CommandRequest{Body: []byte("test command")})
		if err != nil || reply.GetStatusCode() != 200 || string(reply.GetBody()) != "test command" {
			t.Fatalf("command round trip = %v, %v", reply, err)
		}
	}) {
		return
	}
	select {
	case <-session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("test cleanup did not close the panel session")
	}
}
