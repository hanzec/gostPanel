package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"

	"gorm.io/gorm"
)

// RuleSyncService 规则状态同步服务
// 定时从 Gost 节点同步规则的真实运行状态
type RuleSyncService struct {
	nodeRepo      *repository.NodeRepository
	ruleRepo      *repository.RuleRepository
	tunnelRepo    *repository.TunnelRepository
	allowlist     *GostAllowlistService
	syncing       sync.Map
	unsavedChains sync.Map
	ticker        *time.Ticker
	stopChan      chan struct{}
	wg            sync.WaitGroup
}

// NewRuleSyncService 创建规则状态同步服务
func NewRuleSyncService(db *gorm.DB) *RuleSyncService {
	return &RuleSyncService{
		nodeRepo:   repository.NewNodeRepository(db),
		ruleRepo:   repository.NewRuleRepository(db),
		tunnelRepo: repository.NewTunnelRepository(db),
		allowlist:  NewGostAllowlistService(db),
		stopChan:   make(chan struct{}),
	}
}

// Start 启动定时同步任务（每 5 秒）
func (s *RuleSyncService) Start() {
	s.ticker = time.NewTicker(5 * time.Second)
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		logger.Info("规则状态同步服务已启动 (5s 间隔)")

		// 立即执行一次
		s.syncAll()

		for {
			select {
			case <-s.ticker.C:
				s.syncAll()
			case <-s.stopChan:
				logger.Info("规则状态同步服务已停止")
				return
			}
		}
	}()
}

// Stop 停止同步服务
func (s *RuleSyncService) Stop() {
	if s.ticker != nil {
		s.ticker.Stop()
	}
	close(s.stopChan)
	s.wg.Wait()
}

// syncAll 同步所有节点的规则状态
func (s *RuleSyncService) syncAll() {
	nodes, _, err := s.nodeRepo.List(nil)
	if err != nil {
		logger.Errorf("[Sync] 获取节点列表失败: %v", err)
		return
	}

	allowlist, err := s.allowlist.Get(context.Background())
	if err != nil {
		logger.Errorf("[Sync] 读取 Gost 白名单失败: %v", err)
	}
	exitAddresses := make(map[uint]string, len(nodes))
	for _, node := range nodes {
		exitAddresses[node.ID] = node.Address
	}
	for _, node := range nodes {
		// 并发同步每个节点
		go s.syncNodeRules(node, allowlist, exitAddresses)
	}
}

// syncNodeRules 同步单个节点的规则
func (s *RuleSyncService) syncNodeRules(node model.GostNode, allowlist model.GostAllowlist, exitAddresses map[uint]string) {
	// 如果节点离线，跳过规则同步
	if node.Status == model.NodeStatusOffline {
		return
	}
	// A slow Gost update can outlast a sync interval. Finish it before reconciling this node again.
	if _, busy := s.syncing.LoadOrStore(node.ID, struct{}{}); busy {
		return
	}
	defer s.syncing.Delete(node.ID)

	client := utils.GetGostClient(&node)

	// 获取节点真实运行配置
	gostCfg, err := s.getNodeConfig(client, node)
	if err != nil {
		logger.Debugf("[Sync] 获取节点 %d (%s) 配置失败: %v", node.ID, node.Name, err)
		return
	}

	// 提取节点上的 Service 状态
	serviceStates := make(map[string]string)
	for _, svc := range gostCfg.Services {
		state := "stopped"
		if svc.Status != nil {
			state = svc.Status.State
		}
		serviceStates[svc.Name] = state
	}

	// 1. 同步规则状态
	rules, err := s.ruleRepo.FindByNodeID(node.ID)
	if err != nil {
		logger.Errorf("[Sync] 获取节点 %d 规则失败: %v", node.ID, err)
	} else {
		for _, r := range rules {
			s.syncRuleStatus(r, serviceStates)
		}
	}

	tunnels, err := s.tunnelRepo.FindByNodeID(node.ID)
	if err != nil {
		logger.Errorf("[Sync] 获取节点 %d 隧道规则失败: %v", node.ID, err)
		return
	}
	s.recordRelayTraffic(node.ID, tunnels, gostCfg.Services)
	if allowlist.ID != 0 {
		if err = syncGostAllowlist(client, gostCfg, node.ID, tunnels, allowlist); err != nil {
			logger.Errorf("[Sync] 下发 Gost 白名单到节点 %d 失败: %v", node.ID, err)
		}
	}
	s.syncTunnels(client, node.ID, tunnels, gostCfg.Chains, exitAddresses)
}

func (s *RuleSyncService) getNodeConfig(client *gost.Client, node model.GostNode) (*gost.GostConfig, error) {
	if node.TrafficLimitBytes == 0 && node.TrafficQuotaState == "" {
		return client.GetConfig()
	}
	unlock := lockTrafficQuota(node.ID)
	defer unlock()
	if node.TrafficLimitBytes == 0 {
		// Unblock even if the Gost admin API is temporarily down.
		if err := syncTrafficQuota(s.nodeRepo, client, node, nil, time.Now()); err != nil {
			logger.Debugf("[Sync] 清除节点 %d 的流量配额失败: %v", node.ID, err)
		}
		return client.GetConfig()
	}
	current, err := client.GetConfig()
	if err != nil {
		return nil, err
	}
	if err := syncTrafficQuota(s.nodeRepo, client, node, current.Services, time.Now()); err != nil {
		logger.Debugf("[Sync] 同步节点 %d 的流量配额失败: %v", node.ID, err)
	}
	return current, nil
}

func (s *RuleSyncService) syncTunnels(client *gost.Client, nodeID uint, tunnels []model.GostTunnel, chains []gost.ChainConfig, exitAddresses map[uint]string) {
	chainsByName := make(map[string]gost.ChainConfig, len(chains))
	for _, chain := range chains {
		chainsByName[chain.Name] = chain
	}
	changed := false
	for _, t := range tunnels {
		if t.EntryNodeID != nodeID {
			continue
		}
		managedName := tunnelChainName(t.ID)
		chainName := t.ChainID
		if chainName == "" {
			chainName = managedName
		}
		_, exists := chainsByName[chainName]
		if t.EntryRuleID == nil {
			s.syncTunnelStatus(t, exists)
		}
		if !exists || chainName != managedName {
			continue
		}
		exitHost := relayHost(&t, exitAddresses[t.ExitNodeID])
		for _, name := range []string{chainName, clashTunnelChainName(t.ID)} {
			addr, ok := chainsByName[name].TunnelRelayAddress()
			oldHost, _, err := net.SplitHostPort(addr)
			if !ok || err != nil || exitHost == "" || oldHost == exitHost {
				continue
			}
			updated, err := client.UpdateTunnelRelayHost(name, exitHost)
			if err != nil {
				logger.Warnf("[Sync] 更新隧道 %d 转发链 %s 出口地址失败: %v", t.ID, name, err)
				continue
			}
			if updated {
				changed = true
				logger.Infof("[Sync] 隧道 %d 转发链 %s 出口地址变更: %s -> %s", t.ID, name, oldHost, exitHost)
			}
		}
	}
	if _, unsaved := s.unsavedChains.Load(nodeID); changed || unsaved {
		if err := client.SaveConfig(); err != nil {
			s.unsavedChains.Store(nodeID, struct{}{})
			logger.Warnf("[Sync] 保存节点 %d 隧道配置失败: %v", nodeID, err)
		} else {
			s.unsavedChains.Delete(nodeID)
		}
	}
}

// syncRuleStatus 同步规则状态
func (s *RuleSyncService) syncRuleStatus(r model.GostRule, serviceStates map[string]string) {
	serviceID := r.ServiceID
	if serviceID == "" {
		serviceID = fmt.Sprintf("rule-%d", r.ID)
	}

	state := serviceStates[serviceID]
	newStatus := utils.GostStateToRuleStatus(state)

	// 如果状态不一致
	if r.Status != newStatus {
		logger.Infof("[Sync] 规则 %d (%s) 状态变更: %s -> %s (Gost State: %s)", r.ID, r.Name, r.Status, newStatus, state)
		_ = s.ruleRepo.UpdateStatus(r.ID, newStatus)
	}
}

// syncTunnelStatus 同步隧道状态
func (s *RuleSyncService) syncTunnelStatus(t model.GostTunnel, exists bool) {
	var newStatus model.TunnelStatus
	if exists {
		newStatus = model.TunnelStatusRunning
	} else {
		newStatus = model.TunnelStatusStopped
	}

	if t.Status != newStatus {
		logger.Infof("[Sync] 隧道 %d (%s) 状态变更: %s -> %s (Chain Exists: %v)", t.ID, t.Name, t.Status, newStatus, exists)
		_ = s.tunnelRepo.UpdateStatus(t.ID, newStatus)
	}
}
