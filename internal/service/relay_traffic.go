package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
)

func (s *RuleSyncService) recordRelayTraffic(nodeID uint, tunnels []model.GostTunnel, services []gost.ServiceConfig) {
	exits := make(map[string]model.GostTunnel)
	for _, tunnel := range tunnels {
		if tunnel.ExitNodeID != nodeID {
			continue
		}
		name := relayServiceName(tunnel.ID)
		if tunnel.ExitRule != nil && tunnel.ExitRule.Role == model.EndpointRoleExit {
			// Stopped links can retain an old runtime ID. Use the listener owned by the exit endpoint.
			if tunnel.ExitRule.ServiceID == "" || tunnel.ServiceID != tunnel.ExitRule.ServiceID {
				continue
			}
			name = tunnel.ServiceID
		}
		exits[name] = tunnel
	}
	for _, svc := range services {
		tunnel, exists := exits[svc.Name]
		if !exists || svc.Handler == nil || svc.Handler.Type != "relay" || svc.Status == nil || svc.Status.Stats == nil {
			continue
		}
		if err := s.recordRelaySample(tunnel, svc); err != nil {
			logger.Warnf("[Sync] 保存隧道 %d 的出口流量失败: %v", tunnel.ID, err)
		}
	}
}

func (s *RuleSyncService) recordRelaySample(tunnel model.GostTunnel, svc gost.ServiceConfig) error {
	observed := svc.Status.Stats
	created := svc.Status.CreateTime
	if created <= 0 || observed.InputBytes > math.MaxInt64 || observed.OutputBytes > math.MaxInt64 {
		return nil
	}

	previous := tunnel.RelayTraffic
	exit := tunnel.ExitRule
	if exit != nil && exit.Role == model.EndpointRoleExit {
		var err error
		previous, err = s.exitRelayCheckpoint(tunnel, svc.Name)
		if err != nil {
			return err
		}
	}
	lastInput, lastOutput := int64(observed.InputBytes), int64(observed.OutputBytes)
	input, output := lastInput, lastOutput
	if created == previous.ServiceCreatedAt {
		if lastInput == previous.LastInputBytes && lastOutput == previous.LastOutputBytes &&
			(exit == nil || exit.Role != model.EndpointRoleExit || exit.RelayCounterID == svc.Name) {
			return nil
		}
		input = counterIncrease(lastInput, previous.LastInputBytes)
		output = counterIncrease(lastOutput, previous.LastOutputBytes)
	}
	if exit != nil && exit.Role == model.EndpointRoleExit {
		return s.ruleRepo.RecordExitRelayTraffic(exit.ID, svc.Name, input, output, lastInput, lastOutput, created)
	}
	return s.tunnelRepo.RecordRelayTraffic(tunnel.ID, input, output, lastInput, lastOutput, created)
}

func (s *RuleSyncService) exitRelayCheckpoint(tunnel model.GostTunnel, serviceName string) (model.RelayTraffic, error) {
	exit := tunnel.ExitRule
	if exit.RelayCounterID == serviceName {
		return exit.RelayTraffic, nil
	}
	// A running listener can predate exit-level accounting. Its name identifies the old checkpoint owner.
	id, err := strconv.ParseUint(strings.TrimPrefix(serviceName, "relay-tunnel-"), 10, 64)
	if err != nil || relayServiceName(uint(id)) != serviceName {
		return model.RelayTraffic{}, fmt.Errorf("无效的 Relay 服务名 %q", serviceName)
	}
	if uint(id) == tunnel.ID {
		return tunnel.RelayTraffic, nil
	}
	return s.tunnelRepo.HistoricalRelayTraffic(uint(id), exit.ID, tunnel.ExitNodeID)
}
