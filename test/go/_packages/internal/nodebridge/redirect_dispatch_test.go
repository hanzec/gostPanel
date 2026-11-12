package nodebridge

import (
	"testing"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTransparentRedirectRPCIsRegisteredOnNodeStream(t *testing.T) {
	dispatch := controlDispatcher(&controlServer{})
	_, err := dispatch(t.Context(), nodegrpc.V1_ConfigureTransparentRedirect_FullMethodName, nil)
	if status.Code(err) != codes.Unimplemented || status.Convert(err).Message() != "transparent redirect requires a Linux node with a configuration file" {
		t.Fatalf("stream did not reach the transparent redirect environment check: %v", err)
	}
}
