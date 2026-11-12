package service

import (
	"context"
	"time"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tunnelTestTimeout = 10 * time.Second
	tunnelProbeEntry  = "入口节点"
	tunnelProbeExit   = "出口节点"
)

// Test verifies a round trip from the entry node through its saved relay chain
// to a temporary listener on the exit node.
func (s *TunnelService) Test(ctx context.Context, id uint) (int64, error) {
	tunnel, err := s.GetByID(id)
	if err != nil {
		return 0, err
	}
	if tunnel.Status != model.TunnelStatusRunning {
		return 0, errors.ErrTunnelNotRunning
	}
	if tunnel.ChainID == "" {
		return 0, errors.ErrTunnelChainNotFound
	}
	if err := endpointNodesOnline(tunnel); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, tunnelTestTimeout)
	defer cancel()
	endpoint, err := utils.GetGostClient(tunnel.ExitNode).StartTunnelProbe(ctx)
	if err != nil {
		logger.Warnf("隧道 %d 出口测试准备失败: %v", id, err)
		return 0, tunnelProbeError(err, tunnelProbeExit)
	}
	reply, err := utils.GetGostClient(tunnel.EntryNode).ProbeTunnel(ctx, &nodegrpc.TunnelProbeRequest{
		ChainName: tunnel.ChainID, Address: endpoint.GetAddress(), Token: endpoint.GetToken(),
	})
	if err != nil {
		logger.Warnf("隧道 %d 数据往返测试失败: %v", id, err)
		return 0, tunnelProbeError(err, tunnelProbeEntry)
	}
	return reply.GetRoundTripMs(), nil
}

func tunnelProbeError(err error, nodeRole string) error {
	code := status.Code(err)
	switch code {
	case codes.Unimplemented:
		return errors.TunnelProbeDaemonUnsupported(nodeRole)
	case codes.DeadlineExceeded:
		return errors.ErrTunnelProbeTimeout
	}
	if nodeRole == tunnelProbeExit {
		return errors.ErrTunnelProbePrepare
	}
	switch code {
	case codes.NotFound:
		return errors.ErrTunnelChainNotFound
	case codes.FailedPrecondition:
		return errors.ErrTunnelProbeChain
	case codes.Internal:
		return errors.ErrTunnelProbeGost
	case codes.DataLoss:
		return errors.ErrTunnelProbeData
	default:
		return errors.ErrTunnelProbeFailed
	}
}
