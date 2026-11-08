package gost

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (c *Client) GetUpgradeStatus(ctx context.Context) (*nodegrpc.NodeUpgradeStatus, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	return nodegrpc.NewV1Client(conn).GetUpgradeStatus(ctx, &emptypb.Empty{})
}

func (c *Client) StartUpgrade(ctx context.Context, component, version string) (*nodegrpc.NodeUpgradeStatus, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	return nodegrpc.NewV1Client(conn).StartUpgrade(ctx, &nodegrpc.NodeUpgradeRequest{Component: component, Version: version})
}
