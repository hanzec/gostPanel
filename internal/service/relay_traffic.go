package service

import (
	"math"

	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
)

func (s *RuleSyncService) recordRelayTraffic(nodeID uint, tunnels []model.GostTunnel, services []gost.ServiceConfig) {
	exits := make(map[string]model.GostTunnel)
	for _, tunnel := range tunnels {
		if tunnel.ExitNodeID == nodeID {
			exits[relayServiceName(tunnel.ID)] = tunnel
		}
	}
	for _, svc := range services {
		tunnel, exists := exits[svc.Name]
		if !exists || svc.Handler == nil || svc.Handler.Type != "relay" || svc.Status == nil || svc.Status.Stats == nil {
			continue
		}
		previous := tunnel.RelayTraffic
		observed := svc.Status.Stats
		created := svc.Status.CreateTime
		if created <= 0 || observed.InputBytes > math.MaxInt64 || observed.OutputBytes > math.MaxInt64 {
			continue
		}
		lastInput, lastOutput := int64(observed.InputBytes), int64(observed.OutputBytes)
		input, output := lastInput, lastOutput
		if created == previous.ServiceCreatedAt {
			if lastInput == previous.LastInputBytes && lastOutput == previous.LastOutputBytes {
				continue
			}
			input = counterIncrease(lastInput, previous.LastInputBytes)
			output = counterIncrease(lastOutput, previous.LastOutputBytes)
		}
		if err := s.tunnelRepo.RecordRelayTraffic(tunnel.ID, input, output, lastInput, lastOutput, created); err != nil {
			logger.Warnf("[Sync] 保存隧道 %d 的出口流量失败: %v", tunnel.ID, err)
		}
	}
}
