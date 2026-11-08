package gost

import (
	"context"

	"yagp/pkg/nodegrpc"
)

func (c *Client) SyncTrafficQuota(ctx context.Context, limit, used uint64, windowStart int64, tcpPorts, udpPorts []uint32) (uint64, error) {
	reply, err := callNode(ctx, c, nodeMutationTimeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.TrafficQuotaReply, error) {
		return client.SyncTrafficQuota(ctx, &nodegrpc.TrafficQuotaRequest{
			LimitBytes: limit, UsedBytes: used, WindowStart: windowStart, TcpPorts: tcpPorts, UdpPorts: udpPorts,
		})
	})
	return reply.GetUsedBytes(), err
}
