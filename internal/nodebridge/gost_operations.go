package nodebridge

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *controlServer) GetGostServiceStatus(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	if err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	return gostServiceStatus(ctx, s.config.NodeID)
}

func (s *controlServer) RestartGostService(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.GostServiceStatus, error) {
	if err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	return restartGostService(ctx, s.config.NodeID)
}

func (s *controlServer) GetGostLogs(ctx context.Context, req *nodegrpc.GostLogsRequest) (*nodegrpc.GostLogsReply, error) {
	if err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	return gostServiceLogs(ctx, s.config.NodeID, req.GetLines())
}
