package nodebridge

import (
	"context"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *controlServer) GetNftConfig(context.Context, *emptypb.Empty) (*nodegrpc.NftConfig, error) {
	config, err := readNftConfig(s.config.NftConfigFile)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read nftables config: %v", err)
	}
	return &nodegrpc.NftConfig{Config: config}, nil
}

func (s *controlServer) ApplyNftConfig(ctx context.Context, req *nodegrpc.NftConfig) (*nodegrpc.NftConfig, error) {
	if err := applyNftConfig(ctx, s.config.NodeID, s.config.NftConfigFile, req.GetConfig()); err != nil {
		return nil, err
	}
	return req, nil
}

func GetNftTemplate(ctx context.Context, cfg Config) (string, error) {
	conn, err := connectPanel(cfg)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	template, err := nodegrpc.NewV1Client(conn).GetNftTemplate(ctx, &nodegrpc.NftTemplateRequest{NodeId: cfg.NodeID})
	if err != nil {
		return "", err
	}
	return template.GetConfig(), nil
}
