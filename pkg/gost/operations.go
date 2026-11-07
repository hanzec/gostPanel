package gost

import (
	"context"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

const nodeMutationTimeout = 20 * time.Second

func (c *Client) GostServiceStatus(ctx context.Context) (*nodegrpc.GostServiceStatus, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.NodeClient) (*nodegrpc.GostServiceStatus, error) {
		return client.GetGostServiceStatus(ctx, &emptypb.Empty{})
	})
}

func (c *Client) RestartGostService(ctx context.Context) (*nodegrpc.GostServiceStatus, error) {
	return callNode(ctx, c, nodeMutationTimeout, func(ctx context.Context, client nodegrpc.NodeClient) (*nodegrpc.GostServiceStatus, error) {
		return client.RestartGostService(ctx, &emptypb.Empty{})
	})
}

func (c *Client) GostLogs(ctx context.Context) (*nodegrpc.GostLogsReply, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.NodeClient) (*nodegrpc.GostLogsReply, error) {
		return client.GetGostLogs(ctx, &nodegrpc.GostLogsRequest{Lines: 100})
	})
}

func callNode[T any](ctx context.Context, c *Client, timeout time.Duration, call func(context.Context, nodegrpc.NodeClient) (*T, error)) (*T, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return call(ctx, nodegrpc.NewNodeClient(conn))
}
