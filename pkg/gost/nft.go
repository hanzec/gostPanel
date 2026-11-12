package gost

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (c *Client) GetNftConfig(ctx context.Context) (*nodegrpc.NftConfig, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.NftConfig, error) {
		return client.GetNftConfig(ctx, &emptypb.Empty{})
	})
}

func (c *Client) ApplyNftConfig(ctx context.Context, config string) (*nodegrpc.NftConfig, error) {
	return callNode(ctx, c, nodeMutationTimeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.NftConfig, error) {
		return client.ApplyNftConfig(ctx, &nodegrpc.NftConfig{Config: config})
	})
}

func (c *Client) ConfigureTransparentRedirect(ctx context.Context, request *nodegrpc.TransparentRedirectRequest) (*nodegrpc.TransparentRedirectReply, error) {
	return callNode(ctx, c, nodeMutationTimeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.TransparentRedirectReply, error) {
		return client.ConfigureTransparentRedirect(ctx, request)
	})
}
