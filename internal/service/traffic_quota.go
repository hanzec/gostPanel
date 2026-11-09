package service

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Keep a quota update based on an older Gost config from removing a newly started port.
var quotaLocks [32]sync.Mutex

func lockTrafficQuota(nodeID uint) func() {
	lock := &quotaLocks[nodeID%uint(len(quotaLocks))]
	lock.Lock()
	return lock.Unlock
}

func syncTrafficQuota(repo *repository.NodeRepository, client *gost.Client, nodeID uint, services []gost.ServiceConfig, now time.Time) error {
	// The caller may have loaded its node before a calibration acquired the quota lock.
	node, err := repo.FindByID(nodeID)
	if err != nil {
		return err
	}
	window := model.TrafficQuotaWindow(node.TrafficResetPeriod, now)
	var used uint64
	switch {
	case node.TrafficLimitBytes == 0:
		window = model.TrafficQuotaWindowUnset
	case window == node.TrafficQuotaWindowStart:
		used = node.TrafficQuotaUsedBytes
	case window == 0 && node.TotalBytes > 0:
		// Historical Gost totals are only available for the lifetime window.
		used = uint64(node.TotalBytes)
	}

	limit := uint64(node.TrafficLimitBytes)
	var tcp, udp []uint32
	if limit > 0 {
		tcp, udp = quotaPorts(services)
	}
	reported, err := client.SyncTrafficQuota(context.Background(), limit, used, window, tcp, udp)
	if err != nil {
		state := model.TrafficQuotaPending
		if status.Code(err) == codes.Unimplemented {
			if limit == 0 && node.TrafficQuotaWindowStart == model.TrafficQuotaWindowUnset {
				return repo.UpdateTrafficQuota(*node, window, 0, "")
			}
			state = model.TrafficQuotaUnsupported
		}
		if writeErr := repo.UpdateTrafficQuota(*node, node.TrafficQuotaWindowStart, node.TrafficQuotaUsedBytes, state); writeErr != nil {
			logger.Warnf("[Sync] 保存节点 %d 的流量配额状态失败: %v", node.ID, writeErr)
		}
		return err
	}

	state := ""
	if limit > 0 {
		state = model.TrafficQuotaActive
		if reported >= limit {
			state = model.TrafficQuotaBlocked
		}
	}
	return repo.UpdateTrafficQuota(*node, window, reported, state)
}

func quotaPorts(services []gost.ServiceConfig) (tcp, udp []uint32) {
	for _, svc := range services {
		if !isQuotaService(svc) || svc.Listener == nil {
			continue
		}
		if svc.Status != nil && utils.GostStateToRuleStatus(svc.Status.State) != model.RuleStatusRunning {
			continue
		}
		_, port, err := net.SplitHostPort(svc.Addr)
		if err != nil {
			continue
		}
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			continue
		}
		switch svc.Listener.Type {
		case "tcp", "tls", "mtls", "ws", "mws", "red":
			tcp = append(tcp, uint32(value))
		case "udp", "quic":
			udp = append(udp, uint32(value))
		}
	}
	slices.Sort(tcp)
	slices.Sort(udp)
	return slices.Compact(tcp), slices.Compact(udp)
}

func isQuotaService(svc gost.ServiceConfig) bool {
	if svc.Handler == nil {
		return false
	}
	var prefix string
	switch svc.Handler.Type {
	case "forward", "red":
		prefix = "rule-"
	case "relay":
		prefix = "relay-tunnel-"
	default:
		return false
	}
	id, ok := strings.CutPrefix(svc.Name, prefix)
	value, err := strconv.ParseUint(id, 10, 64)
	return ok && err == nil && value != 0
}

func createServiceWithTrafficQuota(repo *repository.NodeRepository, client *gost.Client, node model.GostNode, service *gost.ServiceConfig) error {
	if node.TrafficLimitBytes > 0 {
		unlock := lockTrafficQuota(node.ID)
		defer unlock()
	}
	created, err := client.EnsureService(service)
	if err != nil {
		return err
	}
	if node.TrafficLimitBytes == 0 {
		return nil
	}
	current, err := client.GetConfig()
	if err == nil {
		err = syncTrafficQuota(repo, client, node.ID, current.Services, time.Now())
	}
	if err != nil {
		if created {
			rollbackErr := client.DeleteService(service.Name)
			if rollbackErr != nil {
				logger.Warnf("回滚节点 %d 的转发服务 %s 失败: %v", node.ID, service.Name, rollbackErr)
			}
		}
		logger.Warnf("配置节点 %d 的转发流量配额失败: %v", node.ID, err)
		return errors.ErrNodeTrafficQuotaUnavailable
	}
	return nil
}
