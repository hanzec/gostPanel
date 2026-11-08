package handler

import (
	"context"

	"yagp/pkg/nodegrpc"
)

func (h *ReportHandler) GetNftTemplate(ctx context.Context, request *nodegrpc.NftTemplateRequest) (*nodegrpc.NftConfig, error) {
	node, err := h.authenticateNode(ctx, request.GetNodeId())
	if err != nil {
		return nil, err
	}
	return &nodegrpc.NftConfig{Config: nftTemplate(nodegrpc.NftTableName(uint64(node.ID)))}, nil
}

func (h *streamReports) GetNftTemplate(ctx context.Context, request *nodegrpc.NftTemplateRequest) (*nodegrpc.NftConfig, error) {
	node, err := h.nodeForRequest(ctx, request.GetNodeId())
	if err != nil {
		return nil, err
	}
	return &nodegrpc.NftConfig{Config: nftTemplate(nodegrpc.NftTableName(uint64(node.ID)))}, nil
}
