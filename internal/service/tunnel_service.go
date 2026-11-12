// Package service 提供业务逻辑层服务
package service

import (
	"context"
	"crypto/rand"
	stderrors "errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"yagp/internal/dto"
	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"

	"gorm.io/gorm"
)

// TunnelService 隧道服务
// 负责隧道的 CRUD 操作及启停控制
// 启动隧道时：在出口节点创建 Relay 服务，在入口节点创建 Chain 连接到出口节点
type TunnelService struct {
	tunnelRepo *repository.TunnelRepository
	nodeRepo   *repository.NodeRepository
	logService *LogService
	allowlist  *GostAllowlistService
}

// NewTunnelService 创建隧道服务
func NewTunnelService(db *gorm.DB) *TunnelService {
	return &TunnelService{
		tunnelRepo: repository.NewTunnelRepository(db),
		nodeRepo:   repository.NewNodeRepository(db),
		logService: NewLogService(db),
		allowlist:  NewGostAllowlistService(db),
	}
}

// Create 创建隧道
func (s *TunnelService) Create(req *dto.CreateTunnelReq, username, ip, userAgent string) (*model.GostTunnel, error) {
	nodeIDs, err := s.allowlist.validateNodeIDs(context.Background(), req.AllowlistNodeIDs)
	if err != nil {
		return nil, err
	}
	if req.EntryRuleID != 0 || req.ExitRuleID != 0 {
		if req.EntryNodeID != 0 || req.ExitNodeID != 0 || req.RelayPort != 0 {
			return nil, errors.ErrTunnelEndpointsRequired
		}
		return s.createEndpointTunnel(req, nodeIDs, username, ip, userAgent)
	}
	if req.UseOriginalTarget || len(req.RulesetIDs) != 0 || len(req.ManualDomains) != 0 {
		return nil, errors.ErrTunnelEndpointsRequired
	}
	if err := validateRelayProtocolOptions(req.Protocol, req.ProtocolOptions); err != nil {
		return nil, err
	}
	// 检查入口和出口是否相同
	if req.EntryNodeID == req.ExitNodeID {
		return nil, errors.ErrTunnelNodeSame
	}

	// 检查入口节点是否存在
	entryNode, err := s.nodeRepo.FindByID(req.EntryNodeID)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrEntryNodeNotFound
		}
		return nil, err
	}

	// 检查出口节点是否存在
	exitNode, err := s.nodeRepo.FindByID(req.ExitNodeID)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrExitNodeNotFound
		}
		return nil, err
	}

	// 创建隧道
	tunnel := &model.GostTunnel{
		Name:             req.Name,
		EntryNodeID:      req.EntryNodeID,
		ExitNodeID:       req.ExitNodeID,
		Protocol:         req.Protocol,
		ProtocolOptions:  req.ProtocolOptions,
		RelayPort:        req.RelayPort,
		Remark:           req.Remark,
		Status:           model.TunnelStatusStopped,
		AllowlistNodeIDs: nodeIDs,
		AutoAllowlist:    req.AllowlistNodeIDs == nil,
	}
	if tunnel.AutoAllowlist {
		tunnel.RelaySecret = rand.Text()
	}

	if err = s.tunnelRepo.Create(tunnel); err != nil {
		return nil, err
	}

	// 记录操作日志
	s.logService.Record(
		username,
		model.ActionCreate,
		model.ResourceTypeTunnel,
		tunnel.ID,
		fmt.Sprintf("创建隧道: %s (%s -> %s)", tunnel.Name, entryNode.Name, exitNode.Name),
		ip,
		userAgent)

	logger.Infof("创建隧道成功: %s", tunnel.Name)
	return tunnel, nil
}

// Update 更新隧道（仅支持更新非运行中的隧道，且不能修改入口/出口节点）
func (s *TunnelService) Update(id uint, req *dto.UpdateTunnelReq, username, ip, userAgent string) (*model.GostTunnel, error) {
	tunnel, err := s.tunnelRepo.FindByID(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrTunnelNotFound
		}
		return nil, err
	}

	// 检查是否在运行中
	if tunnel.Status == model.TunnelStatusRunning {
		return nil, errors.ErrTunnelRunning
	}
	if req.AllowlistNodeIDs != nil {
		nodeIDs, err := s.allowlist.validateNodeIDs(context.Background(), req.AllowlistNodeIDs)
		if err != nil {
			return nil, err
		}
		tunnel.AllowlistNodeIDs = nodeIDs
		tunnel.AutoAllowlist = false
	}
	previousProtocol := tunnel.Protocol
	if tunnel.EntryRuleID != nil || req.EntryRuleID != 0 || req.ExitRuleID != 0 {
		if tunnel.EntryRuleID == nil || req.RelayPort != 0 {
			return nil, errors.ErrTunnelEndpointsRequired
		}
		if tunnel.Status == model.TunnelStatusError {
			if err := s.stopEndpointTunnel(tunnel); err != nil {
				return nil, err
			}
		}
		if err := s.setTunnelEndpoints(tunnel, req.EntryRuleID, req.ExitRuleID); err != nil {
			return nil, err
		}
		if tunnel.ExitRule.Role != model.EndpointRoleExit && req.Protocol != "" {
			tunnel.Protocol = req.Protocol
		}
		if err := s.setTunnelTargets(tunnel, req.Targets, req.UseOriginalTarget, req.UseClashRuleset, req.RulesetIDs, req.ManualDomains, req.Strategy); err != nil {
			return nil, err
		}
	} else {
		if (req.UseOriginalTarget != nil && *req.UseOriginalTarget) || len(req.RulesetIDs) != 0 || len(req.ManualDomains) != 0 {
			return nil, errors.ErrTunnelEndpointsRequired
		}
		tunnel.Protocol = req.Protocol
		tunnel.RelayPort = req.RelayPort
	}
	if (tunnel.ExitRule == nil || tunnel.ExitRule.Role != model.EndpointRoleExit) &&
		(req.ProtocolOptions != nil || tunnel.Protocol != previousProtocol) {
		if err := validateRelayProtocolOptions(tunnel.Protocol, req.ProtocolOptions); err != nil {
			return nil, err
		}
		tunnel.ProtocolOptions = req.ProtocolOptions
	}

	// 更新隧道（不能修改入口/出口节点）
	tunnel.Name = req.Name
	tunnel.Remark = req.Remark

	if err = s.tunnelRepo.Update(tunnel, req.AllowlistNodeIDs != nil); err != nil {
		return nil, err
	}

	s.logService.Record(
		username,
		model.ActionUpdate,
		model.ResourceTypeTunnel,
		tunnel.ID,
		fmt.Sprintf("更新隧道: %s", tunnel.Name),
		ip,
		userAgent)

	return tunnel, nil
}

// UpdateAllowlist 只更新白名单，运行中的隧道会在下一轮同步应用。
func (s *TunnelService) UpdateAllowlist(ctx context.Context, id uint, nodeIDs []uint, username, ip, userAgent string) (*model.GostTunnel, error) {
	tunnel, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	nodeIDs, err = s.allowlist.validateNodeIDs(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}
	tunnel.AllowlistNodeIDs = nodeIDs
	tunnel.AutoAllowlist = false
	if err := s.tunnelRepo.UpdateAllowlist(ctx, tunnel); err != nil {
		return nil, err
	}
	s.logService.Record(username, model.ActionUpdate, model.ResourceTypeTunnel, id,
		fmt.Sprintf("更新隧道 IP 白名单: %s", tunnel.Name), ip, userAgent)
	return tunnel, nil
}

// Delete 删除隧道
// 如果有规则正在使用此隧道，不允许删除
func (s *TunnelService) Delete(id uint, username, ip, userAgent string) error {
	tunnel, err := s.tunnelRepo.FindByID(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return errors.ErrTunnelNotFound
		}
		return err
	}

	// 检查是否有规则正在使用此隧道
	hasRules, err := s.tunnelRepo.HasRules(id)
	if err != nil {
		return err
	}
	if hasRules {
		return errors.ErrTunnelHasRules
	}

	// 如果隧道正在运行，先停止
	if tunnel.Status == model.TunnelStatusRunning || (tunnel.EntryRuleID != nil && tunnel.Status == model.TunnelStatusError) {
		if err = s.Stop(id, username, ip, userAgent); err != nil {
			if tunnel.EntryRuleID != nil {
				return err
			}
			logger.Warnf("停止隧道失败: %v", err)
		}
	}

	// 删除隧道
	if err = s.tunnelRepo.Delete(id); err != nil {
		return err
	}

	s.logService.Record(
		username,
		model.ActionDelete,
		model.ResourceTypeTunnel,
		id,
		fmt.Sprintf("删除隧道: %s", tunnel.Name),
		ip,
		userAgent)

	logger.Infof("删除隧道成功: %s", tunnel.Name)
	return nil
}

// GetByID 获取隧道详情
func (s *TunnelService) GetByID(id uint) (*model.GostTunnel, error) {
	tunnel, err := s.tunnelRepo.FindByID(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrTunnelNotFound
		}
		return nil, err
	}
	return tunnel, nil
}

// List 获取隧道列表
func (s *TunnelService) List(req *dto.TunnelListReq) ([]model.GostTunnel, int64, error) {
	req.SetDefaults()

	opt := &repository.QueryOption{
		Pagination: &repository.Pagination{
			Page:     req.Page,
			PageSize: req.PageSize,
		},
		Conditions: make(map[string]any),
	}

	if req.NodeID > 0 {
		opt.Conditions["entry_node_id = ? OR exit_node_id = ?"] = []interface{}{req.NodeID, req.NodeID}
	}
	if req.Status != "" {
		opt.Conditions["status = ?"] = req.Status
	}
	if req.Keyword != "" {
		opt.Conditions["name LIKE ?"] = []interface{}{
			"%" + req.Keyword + "%",
		}
	}

	return s.tunnelRepo.List(opt)
}

// Start 启动隧道
// 在出口节点创建 Relay 服务，在入口节点创建 Chain 连接到出口节点
func (s *TunnelService) Start(id uint, username, ip, userAgent string) error {
	tunnel, err := s.tunnelRepo.FindByID(id)
	if err != nil {
		return err
	}

	// 已在运行中则跳过
	if tunnel.Status == model.TunnelStatusRunning {
		return nil
	}
	if tunnel.EntryRuleID != nil {
		if err := s.startEndpointTunnel(tunnel); err != nil {
			return err
		}
		s.recordTunnelAction(tunnel, model.ActionStart, "启动", username, ip, userAgent)
		return nil
	}

	// 获取入口和出口节点
	entryNode, err := s.nodeRepo.FindByID(tunnel.EntryNodeID)
	if err != nil {
		return errors.ErrEntryNodeNotFound
	}
	exitNode, err := s.nodeRepo.FindByID(tunnel.ExitNodeID)
	if err != nil {
		return errors.ErrExitNodeNotFound
	}

	// 检查节点状态
	if entryNode.Status == model.NodeStatusOffline {
		return errors.ErrEntryNodeOffline
	}
	if exitNode.Status == model.NodeStatusOffline {
		return errors.ErrExitNodeOffline
	}

	// 步骤1：在出口节点创建 Relay 服务
	exitClient := utils.GetGostClient(exitNode)
	relayName := relayServiceName(tunnel.ID)
	var auth *gost.AuthConfig
	if tunnel.RelaySecret != "" {
		auth = &gost.AuthConfig{Username: relayName, Password: tunnel.RelaySecret}
	}

	relaySvc := &gost.ServiceConfig{
		Name:     relayName,
		Addr:     fmt.Sprintf(":%d", tunnel.RelayPort),
		Metadata: map[string]any{"enableStats": true},
		Handler: &gost.HandlerConfig{
			Type: "relay", // relay 协议支持接收客户端传来的目标地址
			Auth: auth,
		},
		Listener: gost.RelayListener(tunnel.Protocol, tunnel.ProtocolOptions),
	}

	if err = s.allowlist.prepareRelay(exitClient, relaySvc, tunnel); err != nil {
		return err
	}
	created, err := createServiceWithTrafficQuota(s.nodeRepo, exitClient, *exitNode, relaySvc)
	if err != nil {
		_ = s.tunnelRepo.UpdateStatus(id, model.TunnelStatusError)
		if stderrors.Is(err, errors.ErrNodeTrafficQuotaUnavailable) {
			return err
		}
		return errors.ErrTunnelRelayCreateFailed
	}
	if !created {
		if _, err = exitClient.SetRelayAllowlist(relaySvc.Name, relaySvc.Admission != ""); err != nil {
			return err
		}
	}

	// 保存出口节点配置
	_ = exitClient.SaveConfig()

	// 步骤2：在入口节点创建 Chain 连接到出口节点的 Relay 服务
	entryClient := utils.GetGostClient(entryNode)

	// 从出口节点配置中获取主机 IP
	exitHost := exitNode.Address
	if exitHost == "" {
		// 回滚：删除出口节点的 Relay 服务
		_ = exitClient.DeleteService(relayName)
		_ = exitClient.SaveConfig()
		_ = s.tunnelRepo.UpdateStatus(id, model.TunnelStatusError)
		return errors.ErrExtractHostFailed
	}

	chainName := tunnelChainName(tunnel.ID)
	relayAddr := net.JoinHostPort(strings.Trim(exitHost, "[]"), strconv.Itoa(tunnel.RelayPort))

	chain := newTunnelChain(chainName, relayAddr, tunnel.Protocol, tunnel.ProtocolOptions, auth)

	if err = entryClient.CreateChain(chain); err != nil {
		// 回滚：删除出口节点的 Relay 服务
		_ = exitClient.DeleteService(relayName)
		_ = exitClient.SaveConfig()
		_ = s.tunnelRepo.UpdateStatus(id, model.TunnelStatusError)
		return errors.ErrTunnelChainCreateFailed
	}

	// 保存入口节点配置
	_ = entryClient.SaveConfig()

	// 更新隧道状态和服务 ID
	_ = s.tunnelRepo.UpdateServiceInfo(id, relayName, chainName)
	_ = s.tunnelRepo.UpdateStatus(id, model.TunnelStatusRunning)

	s.logService.Record(
		username,
		model.ActionStart,
		model.ResourceTypeTunnel,
		id,
		fmt.Sprintf("启动隧道: %s", tunnel.Name),
		ip,
		userAgent)

	logger.Infof("启动隧道成功: %s (Relay: %s -> Chain: %s)", tunnel.Name, relayName, chainName)
	return nil
}

func tunnelChainName(id uint) string {
	return fmt.Sprintf("tunnel-%d-chain", id)
}

func newTunnelChain(name, address, protocol string, options map[string]string, auth *gost.AuthConfig) *gost.ChainConfig {
	return &gost.ChainConfig{Name: name, Hops: []*gost.HopConfig{{Name: "hop-0", Nodes: []*gost.NodeConfig{{
		Name: "exit-relay", Addr: address,
		Connector: &gost.ConnectorConfig{Type: "relay", Auth: auth},
		Dialer:    gost.RelayDialer(protocol, options),
	}}}}}
}

// Stop 停止隧道
// 删除入口节点的 Chain 和出口节点的 Relay 服务
func (s *TunnelService) Stop(id uint, username, ip, userAgent string) error {
	tunnel, err := s.tunnelRepo.FindByID(id)
	if err != nil {
		return err
	}
	if tunnel.EntryRuleID != nil {
		if tunnel.Status == model.TunnelStatusStopped {
			return nil
		}
		if err := s.stopEndpointTunnel(tunnel); err != nil {
			return err
		}
		s.recordTunnelAction(tunnel, model.ActionStop, "停止", username, ip, userAgent)
		return nil
	}

	// 未运行则跳过
	if tunnel.Status != model.TunnelStatusRunning {
		return nil
	}

	// 获取入口和出口节点
	entryNode, _ := s.nodeRepo.FindByID(tunnel.EntryNodeID)
	exitNode, _ := s.nodeRepo.FindByID(tunnel.ExitNodeID)

	// 步骤1：删除入口节点的 Chain
	if entryNode != nil && entryNode.Status == model.NodeStatusOnline && tunnel.ChainID != "" {
		entryClient := utils.GetGostClient(entryNode)
		if err = entryClient.DeleteChain(tunnel.ChainID); err != nil {
			logger.Warnf("删除隧道 Chain 失败: %v", err)
		}
		if err = entryClient.DeleteChain(clashTunnelChainName(id)); err != nil {
			logger.Warnf("删除隧道 Clash 分流链失败: %v", err)
		}
		_ = entryClient.SaveConfig()
	}

	// 步骤2：删除出口节点的 Relay 服务
	if exitNode != nil && exitNode.Status == model.NodeStatusOnline && tunnel.ServiceID != "" {
		exitClient := utils.GetGostClient(exitNode)
		if err = exitClient.DeleteService(tunnel.ServiceID); err != nil {
			logger.Warnf("删除隧道 Relay 服务失败: %v", err)
		}
		_ = exitClient.SaveConfig()
	}

	// 更新状态
	_ = s.tunnelRepo.UpdateStatus(id, model.TunnelStatusStopped)

	s.logService.Record(
		username,
		model.ActionStop,
		model.ResourceTypeTunnel,
		id,
		fmt.Sprintf("停止隧道: %s", tunnel.Name),
		ip,
		userAgent)

	logger.Infof("停止隧道成功: %s", tunnel.Name)
	return nil
}

// GetChainID 获取隧道的 Chain ID（供规则服务使用）
func (s *TunnelService) GetChainID(tunnelID uint) (string, error) {
	tunnel, err := s.tunnelRepo.FindByID(tunnelID)
	if err != nil {
		return "", err
	}
	return tunnel.ChainID, nil
}

// GetEntryNodeID 获取隧道的入口节点 ID（供规则服务使用）
func (s *TunnelService) GetEntryNodeID(tunnelID uint) (uint, error) {
	tunnel, err := s.tunnelRepo.FindByID(tunnelID)
	if err != nil {
		return 0, err
	}
	return tunnel.EntryNodeID, nil
}
