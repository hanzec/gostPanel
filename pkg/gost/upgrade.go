package gost

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (c *Client) GetUpgradeStatus(ctx context.Context) (*nodegrpc.NodeUpgradeStatus, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.NodeUpgradeStatus, error) {
		return client.GetUpgradeStatus(ctx, &emptypb.Empty{})
	})
}

func (c *Client) StartUpgrade(ctx context.Context, component, version string) (*nodegrpc.NodeUpgradeStatus, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.NodeUpgradeStatus, error) {
		return client.StartUpgrade(ctx, &nodegrpc.NodeUpgradeRequest{Component: component, Version: version})
	})
}
