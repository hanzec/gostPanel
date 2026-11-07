//go:build !libsystemd || !linux || (!amd64 && !arm64)

package nodebridge

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const systemdUnsupported = "Gost service management requires a Linux systemd node build"

func gostServiceStatus(context.Context, uint64) (*nodegrpc.GostServiceStatus, error) {
	return nil, status.Error(codes.Unimplemented, systemdUnsupported)
}

func restartGostService(context.Context, uint64) (*nodegrpc.GostServiceStatus, error) {
	return nil, status.Error(codes.Unimplemented, systemdUnsupported)
}

func gostServiceLogs(context.Context, uint64, uint32) (*nodegrpc.GostLogsReply, error) {
	return nil, status.Error(codes.Unimplemented, systemdUnsupported)
}
