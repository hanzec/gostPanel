package service

import (
	"context"
	"time"

	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *NodeService) CalibrateNodeTotals(ctx context.Context, id uint, input, output int64) error {
	if _, err := s.GetByID(id); err != nil {
		return err
	}
	return s.nodeRepo.SetStats(ctx, id, input, output)
}

func (s *NodeService) CalibrateNodeQuota(ctx context.Context, id uint, used uint64) error {
	unlock := lockTrafficQuota(id)
	defer unlock()
	node, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if node.TrafficLimitBytes <= 0 || node.Status != model.NodeStatusOnline {
		return status.Error(codes.FailedPrecondition, "校准配额用量需要在线且已设置流量上限的节点")
	}
	client := utils.GetGostClient(node)
	config, err := client.GetConfig()
	if err != nil {
		return err
	}
	window := model.TrafficQuotaWindow(node.TrafficResetPeriod, time.Now())
	var previous uint64
	if node.TrafficQuotaWindowStart == window {
		previous = node.TrafficQuotaUsedBytes
	}
	limit := uint64(node.TrafficLimitBytes)
	tcp, udp := quotaPorts(config.Services)
	reported, err := client.CalibrateTrafficQuota(ctx, &nodegrpc.TrafficQuotaRequest{
		LimitBytes: limit, UsedBytes: previous, WindowStart: window, TcpPorts: tcp, UdpPorts: udp,
		CalibratedUsedBytes: &used,
	})
	if err != nil {
		return err
	}
	state := model.TrafficQuotaActive
	if reported >= limit {
		state = model.TrafficQuotaBlocked
	}
	return s.nodeRepo.UpdateTrafficQuota(*node, window, reported, state)
}
