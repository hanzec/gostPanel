package gost

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (c *Client) SyncTrafficQuota(ctx context.Context, limit, used uint64, windowStart int64, tcpPorts, udpPorts []uint32) (uint64, error) {
	reply, err := c.updateTrafficQuota(ctx, &nodegrpc.TrafficQuotaRequest{
		LimitBytes: limit, UsedBytes: used, WindowStart: windowStart, TcpPorts: tcpPorts, UdpPorts: udpPorts,
	})
	return reply.GetUsedBytes(), err
}

func (c *Client) CalibrateTrafficQuota(ctx context.Context, request *nodegrpc.TrafficQuotaRequest) (uint64, error) {
	reply, err := c.updateTrafficQuota(ctx, request)
	if err == nil && !reply.GetCalibrated() {
		return 0, status.Error(codes.Unimplemented, "请升级节点守护程序后再校准配额用量")
	}
	return reply.GetUsedBytes(), err
}

func (c *Client) updateTrafficQuota(ctx context.Context, request *nodegrpc.TrafficQuotaRequest) (*nodegrpc.TrafficQuotaReply, error) {
	return callNode(ctx, c, nodeMutationTimeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.TrafficQuotaReply, error) {
		return client.SyncTrafficQuota(ctx, request)
	})
}
