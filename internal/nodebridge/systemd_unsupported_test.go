//go:build !libsystemd || !linux || (!amd64 && !arm64)

package nodebridge

import (
	"context"
	"testing"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSystemdManagementRequiresSupportedBuild(t *testing.T) {
	control := controlServer{config: Config{NodeID: 42, Username: controlTestUser, Password: controlTestPassword}}
	for _, test := range []struct {
		name string
		call func(context.Context) error
	}{
		{"status", func(ctx context.Context) error {
			_, err := control.GetGostServiceStatus(ctx, &emptypb.Empty{})
			return err
		}},
		{"restart", func(ctx context.Context) error {
			_, err := control.RestartGostService(ctx, &emptypb.Empty{})
			return err
		}},
		{"logs", func(ctx context.Context) error {
			_, err := control.GetGostLogs(ctx, &nodegrpc.GostLogsRequest{Lines: 10})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(t.Context()); status.Code(err) != codes.Unimplemented {
				t.Fatalf("unsupported build returned %v", err)
			}
		})
	}
}
