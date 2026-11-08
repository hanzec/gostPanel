package nodebridge

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *controlServer) GetGostServiceStatus(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	return gostServiceStatus(ctx, s.config.NodeID)
}

func (s *controlServer) RestartGostService(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	return restartGostService(ctx, s.config.NodeID)
}

func (s *controlServer) GetGostLogs(ctx context.Context, req *nodegrpc.GostLogsRequest) (*nodegrpc.GostLogsReply, error) {
	return gostServiceLogs(ctx, s.config.NodeID, req.GetLines())
}
