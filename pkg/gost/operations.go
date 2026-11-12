package gost

import (
	"context"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

const nodeMutationTimeout = 20 * time.Second

func (c *Client) CheckListener(ctx context.Context, protocol, ip string, port uint32) (*nodegrpc.ListenerCheckReply, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.ListenerCheckReply, error) {
		return client.CheckListener(ctx, &nodegrpc.ListenerCheckRequest{Protocol: protocol, Ip: ip, Port: port})
	})
}

func (c *Client) StartTunnelProbe(ctx context.Context) (*nodegrpc.TunnelProbeEndpoint, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.TunnelProbeEndpoint, error) {
		return client.StartTunnelProbe(ctx, &emptypb.Empty{})
	})
}

func (c *Client) ProbeTunnel(ctx context.Context, req *nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error) {
	return callNode(ctx, c, 9*time.Second, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.TunnelProbeReply, error) {
		return client.ProbeTunnel(ctx, req)
	})
}

func (c *Client) GostServiceStatus(ctx context.Context) (*nodegrpc.GostServiceStatus, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.GostServiceStatus, error) {
		return client.GetGostServiceStatus(ctx, &emptypb.Empty{})
	})
}

func (c *Client) RestartGostService(ctx context.Context) (*nodegrpc.GostServiceStatus, error) {
	return callNode(ctx, c, nodeMutationTimeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.GostServiceStatus, error) {
		return client.RestartGostService(ctx, &emptypb.Empty{})
	})
}

func (c *Client) GostLogs(ctx context.Context) (*nodegrpc.GostLogsReply, error) {
	return callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.GostLogsReply, error) {
		return client.GetGostLogs(ctx, &nodegrpc.GostLogsRequest{Lines: 100})
	})
}

func callNode[T any](ctx context.Context, c *Client, timeout time.Duration, call func(context.Context, nodegrpc.V1Client) (*T, error)) (*T, error) {
	client, err := c.connect()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return call(ctx, client)
}
