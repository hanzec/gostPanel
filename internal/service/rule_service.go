// Package service 提供业务逻辑层服务
package service

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"yagp/internal/config"
	"yagp/internal/dto"
	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"

	"gorm.io/gorm"
)

// RuleService 管理节点入口和旧的隧道转发规则。
type RuleService struct {
	ruleRepo       *repository.RuleRepository
	nodeRepo       *repository.NodeRepository
	tunnelRepo     *repository.TunnelRepository
	logService     *LogService
	rulesetService *GostRulesetService
}

// NewRuleService 创建规则服务
func NewRuleService(db *gorm.DB) *RuleService {
	return &RuleService{
		ruleRepo:       repository.NewRuleRepository(db),
		nodeRepo:       repository.NewNodeRepository(db),
		tunnelRepo:     repository.NewTunnelRepository(db),
		logService:     NewLogService(db),
		rulesetService: NewGostRulesetService(db),
	}
}

// Create 创建规则。新入口使用 NodeID，旧的隧道规则使用 TunnelID。
func (s *RuleService) Create(req *dto.CreateRuleReq, username, ip, userAgent string) (*model.GostRule, error) {
	kind := model.RuleType(req.Type)
	if kind == "" {
		kind = model.RuleTypeForward
	}
	var entryNodeID uint
	var nodeID, tunnelID *uint
	switch kind {
	case model.RuleTypeForward:
		node, err := s.findRuleNode(req.NodeID)
		if err != nil {
			return nil, err
		}
		nodeID, entryNodeID = req.NodeID, node.ID
	case model.RuleTypeTunnel:
		if req.TunnelID == nil || *req.TunnelID == 0 {
			return nil, errors.ErrTunnelRequired
		}
		tunnel, err := s.tunnelRepo.FindByID(*req.TunnelID)
		if err != nil {
			if stderrors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errors.ErrTunnelNotFound
			}
			return nil, err
		}
		tunnelID, entryNodeID = req.TunnelID, tunnel.EntryNodeID
	default:
		return nil, errors.ErrRuleTypeInvalid
	}

	listenIP, err := normalizeRuleListenIP(model.RuleProtocol(req.Protocol), req.ListenIP)
	if err != nil {
		return nil, err
	}
	if err = validateEndpointConfig(model.EndpointRole(req.Role), kind, model.RuleProtocol(req.Protocol), listenIP, req.Targets, req.UseOriginalTarget, req.UseClashRuleset); err != nil {
		return nil, err
	}
	if !req.UseOriginalTarget && (len(req.RulesetIDs) != 0 || len(req.ManualDomains) != 0) {
		return nil, errors.ErrEndpointTargets
	}
	rulesetIDs, manualDomains, err := s.rulesetService.NormalizeSelection(context.Background(), req.RulesetIDs, req.ManualDomains)
	if err != nil {
		return nil, err
	}
	if err := validateEndpointProtocolOptions(model.EndpointRole(req.Role), req.Protocol, req.ProtocolOptions); err != nil {
		return nil, err
	}
	exists, err := s.ruleRepo.ListenerConflicts(entryNodeID, model.RuleProtocol(req.Protocol), listenIP, req.ListenPort)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.ErrRulePortExists
	}

	// 创建规则
	rule := &model.GostRule{
		NodeID:            nodeID,
		TunnelID:          tunnelID,
		Name:              req.Name,
		Type:              kind,
		Role:              model.EndpointRole(req.Role),
		Protocol:          model.RuleProtocol(req.Protocol),
		ProtocolOptions:   req.ProtocolOptions,
		ListenIP:          listenIP,
		ListenPort:        req.ListenPort,
		Targets:           req.Targets,
		UseClashRuleset:   req.UseClashRuleset,
		UseOriginalTarget: req.UseOriginalTarget,
		RulesetIDs:        rulesetIDs, ManualDomains: manualDomains,
		Strategy:  req.Strategy,
		EnableTLS: req.EnableTLS,
		Remark:    req.Remark,
		Status:    model.RuleStatusStopped,
	}

	if err = s.ruleRepo.Create(rule); err != nil {
		return nil, err
	}

	// 记录操作日志
	s.logService.Record(
		username,
		model.ActionCreate,
		model.ResourceTypeRule,
		rule.ID,
		fmt.Sprintf("创建规则: %s (类型: %s)", rule.Name, rule.Type),
		ip,
		userAgent)

	logger.Infof("创建规则成功: %s (%s)", rule.Name, net.JoinHostPort(rule.ListenIP, strconv.Itoa(rule.ListenPort)))
	return rule, nil
}

// Update also permits policy changes on an exit whose tunnel has stopped.
func (s *RuleService) Update(id uint, req *dto.UpdateRuleReq, username, ip, userAgent string) (*model.GostRule, error) {
	rule, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if rule.LinkedTunnelID != nil && (rule.Role != model.EndpointRoleExit || rule.LinkedTunnelStatus != model.TunnelStatusStopped) {
		return nil, errors.ErrRuleTunnelLinked
	}

	// 检查是否在运行中
	if rule.Status == model.RuleStatusRunning {
		return nil, errors.ErrRuleRunning
	}
	if rule.LinkedTunnelID != nil && ((req.NodeID != nil && (rule.NodeID == nil || *req.NodeID != *rule.NodeID)) ||
		(req.Role != "" && model.EndpointRole(req.Role) != rule.Role) || req.ListenPort != rule.ListenPort ||
		(req.ListenIP != nil && *req.ListenIP != rule.ListenIP)) {
		return nil, errors.ErrRuleTunnelLinked
	}

	// 获取入口节点 ID（用于端口冲突检查）
	entryNodeID := ruleEntryNodeID(rule)
	if rule.Type == model.RuleTypeForward && req.NodeID != nil {
		node, err := s.findRuleNode(req.NodeID)
		if err != nil {
			return nil, err
		}
		rule.Node, rule.NodeID, entryNodeID = node, req.NodeID, node.ID
	}

	listenIP := rule.ListenIP
	if req.ListenIP != nil {
		listenIP = *req.ListenIP
	} else if req.Protocol == string(model.RuleProtocolTransparent) && rule.Protocol != model.RuleProtocolTransparent {
		listenIP = ""
	}
	listenIP, err = normalizeRuleListenIP(model.RuleProtocol(req.Protocol), listenIP)
	if err != nil {
		return nil, err
	}
	useClashRuleset := rule.UseClashRuleset
	if req.UseClashRuleset != nil {
		useClashRuleset = *req.UseClashRuleset
	}
	useOriginalTarget := rule.UseOriginalTarget
	if req.UseOriginalTarget != nil {
		useOriginalTarget = *req.UseOriginalTarget
	}
	rulesetIDs, manualDomains := rule.RulesetIDs, rule.ManualDomains
	if req.RulesetIDs != nil {
		rulesetIDs = req.RulesetIDs
	}
	if req.ManualDomains != nil {
		manualDomains = req.ManualDomains
	}
	targets := rule.Targets
	if req.Targets != nil {
		targets = req.Targets
	}
	role := rule.Role
	if req.Role != "" {
		role = model.EndpointRole(req.Role)
	}
	if role == model.EndpointRoleEntry {
		if len(req.Targets) != 0 || (req.UseClashRuleset != nil && *req.UseClashRuleset) ||
			(req.UseOriginalTarget != nil && *req.UseOriginalTarget) || len(req.RulesetIDs) != 0 || len(req.ManualDomains) != 0 {
			return nil, errors.ErrEndpointTargets
		}
		targets, useClashRuleset = []string{}, false
		useOriginalTarget, rulesetIDs, manualDomains = false, nil, nil
	}
	// Listener-only edits preserve targets belonging to an unconverted legacy endpoint.
	if role != "" || req.UseClashRuleset != nil || req.UseOriginalTarget != nil || useOriginalTarget || req.Protocol == "red" || req.RulesetIDs != nil || req.ManualDomains != nil || req.Targets != nil || req.Strategy != "" {
		if err = validateEndpointConfig(role, rule.Type, model.RuleProtocol(req.Protocol), listenIP, targets, useOriginalTarget, useClashRuleset); err != nil {
			return nil, err
		}
	} else if req.Protocol != "" && req.Protocol != "tcp" && req.Protocol != "udp" && req.Protocol != "red" {
		return nil, errors.ErrEndpointProtocol
	}
	if !useOriginalTarget && (len(rulesetIDs) != 0 || len(manualDomains) != 0) {
		return nil, errors.ErrEndpointTargets
	}
	if req.RulesetIDs != nil || req.ManualDomains != nil {
		rulesetIDs, manualDomains, err = s.rulesetService.NormalizeSelection(context.Background(), rulesetIDs, manualDomains)
		if err != nil {
			return nil, err
		}
	}
	if req.ProtocolOptions != nil || req.Protocol != string(rule.Protocol) || role != rule.Role {
		if err := validateEndpointProtocolOptions(role, req.Protocol, req.ProtocolOptions); err != nil {
			return nil, err
		}
		rule.ProtocolOptions = req.ProtocolOptions
	}
	exists, err := s.ruleRepo.ListenerConflicts(entryNodeID, model.RuleProtocol(req.Protocol), listenIP, req.ListenPort, id)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.ErrRulePortExists
	}

	rule.Name = req.Name
	rule.Role = role
	rule.Protocol = model.RuleProtocol(req.Protocol)
	rule.ListenIP = listenIP
	rule.ListenPort = req.ListenPort
	rule.Targets = targets
	rule.UseClashRuleset = useClashRuleset
	rule.UseOriginalTarget, rule.RulesetIDs, rule.ManualDomains = useOriginalTarget, rulesetIDs, manualDomains
	if req.Strategy != "" {
		rule.Strategy = req.Strategy
	}
	if role == model.EndpointRoleEntry {
		rule.Strategy = "round"
	}
	rule.EnableTLS = req.EnableTLS
	rule.Remark = req.Remark

	if err = s.ruleRepo.Update(rule); err != nil {
		return nil, err
	}

	s.logService.Record(
		username,
		model.ActionUpdate,
		model.ResourceTypeRule,
		rule.ID,
		fmt.Sprintf("更新规则: %s", rule.Name),
		ip,
		userAgent)

	return rule, nil
}

func (s *RuleService) findRuleNode(id *uint) (*model.GostNode, error) {
	if id == nil || *id == 0 {
		return nil, errors.ErrNodeRequired
	}
	node, err := s.nodeRepo.FindByID(*id)
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.ErrNodeNotFound
	}
	return node, err
}

func validateEndpointConfig(role model.EndpointRole, kind model.RuleType, protocol model.RuleProtocol, listenIP string, targets []string, useOriginal, useClash bool) error {
	if useOriginal && useClash {
		return errors.ErrEndpointTargets
	}
	if role != "" && (kind != model.RuleTypeForward || (role != model.EndpointRoleEntry && role != model.EndpointRoleExit)) {
		return errors.ErrEndpointRole
	}
	if ((role == model.EndpointRoleExit || kind == model.RuleTypeTunnel) && protocol == model.RuleProtocolTransparent) ||
		(role != model.EndpointRoleExit && protocol != "" && protocol != model.RuleProtocolTCP && protocol != model.RuleProtocolUDP && protocol != model.RuleProtocolTransparent) {
		return errors.ErrEndpointProtocol
	}
	switch role {
	case model.EndpointRoleEntry:
		if len(targets) != 0 || useOriginal || useClash {
			return errors.ErrEndpointTargets
		}
	case model.EndpointRoleExit:
		if !relayBindReachable(listenIP) {
			return errors.ErrTunnelExitInvalid
		}
		if ((useOriginal || useClash) && len(targets) != 0) || (!useOriginal && !useClash && len(targets) == 0) {
			return errors.ErrEndpointTargets
		}
		for _, target := range targets {
			if strings.TrimSpace(target) == "" {
				return errors.ErrEndpointTargets
			}
		}
	default:
		return validateTransparentTarget(useOriginal, useClash, protocol, listenIP, targets)
	}
	return nil
}

func normalizeRuleListenIP(protocol model.RuleProtocol, value string) (string, error) {
	value = strings.TrimSpace(value)
	if protocol == model.RuleProtocolTransparent {
		if value != "" {
			addr, err := netip.ParseAddr(value)
			if err != nil || !addr.IsLoopback() || addr.Zone() != "" {
				return "", errors.ErrRuleTransparentListenIP
			}
		}
		return "127.0.0.1", nil
	}
	if value == "" {
		return "", nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return "", errors.ErrRuleListenIPInvalid
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		return "", nil
	}
	return addr.String(), nil
}

// Delete 删除规则
func (s *RuleService) Delete(id uint, username, ip, userAgent string) error {
	rule, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if rule.LinkedTunnelID != nil {
		return errors.ErrRuleTunnelLinked
	}

	// A failed start or stop can retain a live service even when the status is not running.
	if rule.Status == model.RuleStatusRunning || rule.ServiceID != "" {
		if err = s.Stop(id, username, ip, userAgent); err != nil {
			return err
		}
	}

	// 删除规则
	if err = s.ruleRepo.Delete(id); err != nil {
		return err
	}

	s.logService.Record(
		username,
		model.ActionDelete,
		model.ResourceTypeRule,
		id,
		fmt.Sprintf("删除规则: %s", rule.Name),
		ip,
		userAgent)

	logger.Infof("删除规则成功: %s", rule.Name)
	return nil
}

// GetByID 获取规则详情
func (s *RuleService) GetByID(id uint) (*model.GostRule, error) {
	rule, err := s.ruleRepo.FindByID(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrRuleNotFound
		}
		return nil, err
	}
	if err := s.loadEntranceBinding(rule); err != nil {
		return nil, err
	}
	return rule, nil
}

func (s *RuleService) loadEntranceBinding(rule *model.GostRule) error {
	tunnel, err := s.tunnelRepo.FindByEntranceID(rule.ID)
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if tunnel.EntryRuleID != nil && *tunnel.EntryRuleID == rule.ID {
		setEntranceBinding(rule, tunnel.ID, "entry", tunnel.Status)
	} else {
		setEntranceBinding(rule, tunnel.ID, "exit", tunnel.Status)
	}
	return nil
}

func setEntranceBinding(rule *model.GostRule, tunnelID uint, role string, status model.TunnelStatus) {
	if rule != nil {
		rule.LinkedTunnelID, rule.TunnelRole, rule.LinkedTunnelStatus = &tunnelID, role, status
	}
}

// List 获取规则列表
func (s *RuleService) List(req *dto.RuleListReq) ([]model.GostRule, int64, error) {
	req.SetDefaults()

	opt := &repository.QueryOption{
		Pagination: &repository.Pagination{
			Page:     req.Page,
			PageSize: req.PageSize,
		},
		Conditions: make(map[string]any),
	}

	if req.NodeID > 0 {
		opt.Conditions["node_id = ?"] = req.NodeID
	}
	if req.TunnelID > 0 {
		opt.Conditions["tunnel_id = ?"] = req.TunnelID
	}
	if req.Type != "" {
		opt.Conditions["type = ?"] = req.Type
	}
	if req.Status != "" {
		opt.Conditions["status = ?"] = req.Status
	}
	if req.Keyword != "" {
		opt.Conditions["name LIKE ?"] = []interface{}{
			"%" + req.Keyword + "%",
		}
	}

	rules, total, err := s.ruleRepo.List(opt)
	if err != nil || len(rules) == 0 {
		return rules, total, err
	}
	ids := make([]uint, 0, len(rules))
	byID := make(map[uint]*model.GostRule, len(rules))
	for i := range rules {
		ids = append(ids, rules[i].ID)
		byID[rules[i].ID] = &rules[i]
	}
	bindings, err := s.ruleRepo.EntranceBindings(ids)
	if err != nil {
		return nil, 0, err
	}
	for _, tunnel := range bindings {
		if tunnel.EntryRuleID != nil {
			setEntranceBinding(byID[*tunnel.EntryRuleID], tunnel.ID, "entry", tunnel.Status)
		}
		if tunnel.ExitRuleID != nil {
			setEntranceBinding(byID[*tunnel.ExitRuleID], tunnel.ID, "exit", tunnel.Status)
		}
	}
	return rules, total, nil
}

// Start 启动规则
func (s *RuleService) Start(id uint, username, ip, userAgent string) error {
	rule, err := s.ruleRepo.FindByID(id)
	if err != nil {
		return err
	}
	if err := s.requireUnlinkedEntrance(rule); err != nil {
		return err
	}

	// 已在运行中则跳过
	if rule.Status == model.RuleStatusRunning {
		return nil
	}
	if rule.Type == model.RuleTypeForward && (rule.Role != "" || rule.Protocol == model.RuleProtocolTransparent || rule.UseOriginalTarget || rule.UseClashRuleset || len(rule.Targets) == 0) {
		return errors.ErrRuleTargetRequired
	}

	// 获取入口节点
	entryNodeID := ruleEntryNodeID(rule)
	node, err := s.nodeRepo.FindByID(entryNodeID)
	if err != nil {
		return errors.ErrNodeNotFound
	}
	if node.Status == model.NodeStatusOffline {
		return errors.ErrNodeOffline
	}

	// 根据规则类型处理
	if rule.Type == model.RuleTypeTunnel {
		if err = s.startTunnelRule(rule, node); err != nil {
			return err
		}
	} else {
		if err = s.buildAndStartService(utils.GetGostClient(node), node, rule, "", rule.Targets); err != nil {
			return err
		}
	}

	s.logService.Record(
		username,
		model.ActionStart,
		model.ResourceTypeRule,
		id,
		fmt.Sprintf("启动规则: %s", rule.Name),
		ip,
		userAgent)

	logger.Infof("启动规则成功: %s", rule.Name)
	return nil
}

func (s *RuleService) requireUnlinkedEntrance(rule *model.GostRule) error {
	if err := s.loadEntranceBinding(rule); err != nil {
		return err
	}
	if rule.LinkedTunnelID != nil {
		return errors.ErrRuleTunnelLinked
	}
	return nil
}

// startTunnelRule 启动隧道转发规则（通过隧道链路）
func (s *RuleService) startTunnelRule(rule *model.GostRule, node *model.GostNode) error {
	if rule.TunnelID == nil {
		return errors.ErrTunnelRequired
	}
	if rule.Protocol == model.RuleProtocolTransparent {
		return errors.ErrEndpointProtocol
	}
	if err := validateTransparentTarget(rule.UseOriginalTarget, rule.UseClashRuleset, rule.Protocol, rule.ListenIP, rule.Targets); err != nil {
		return err
	}

	// 获取隧道信息
	tunnel, err := s.tunnelRepo.FindByID(*rule.TunnelID)
	if err != nil {
		return errors.ErrTunnelNotFound
	}

	// 确保隧道已启动
	if tunnel.Status != model.TunnelStatusRunning {
		return errors.ErrTunnelNotRunning
	}
	return s.startSelectedEntrance(rule, node, tunnel, nil)
}

// startSelectedEntrance provisions the ingress against a tunnel's ready relay chain.
func (s *RuleService) startSelectedEntrance(rule *model.GostRule, node *model.GostNode, tunnel *model.GostTunnel, targets []string) error {
	client := utils.GetGostClient(node)
	if err := s.provisionSelectedEntrance(client, rule, node, tunnel, targets, gost.ClashRedirectMark); err != nil {
		return err
	}
	return s.persistStartedService(client, rule, rule.Type == model.RuleTypeForward)
}

func (s *RuleService) provisionSelectedEntrance(client *gost.Client, rule *model.GostRule, node *model.GostNode, tunnel *model.GostTunnel, targets []string, mark uint32) error {
	if tunnel.ChainID == "" {
		_ = s.ruleRepo.UpdateStatus(rule.ID, model.RuleStatusError)
		return errors.ErrTunnelChainNotFound
	}

	chainID := tunnel.ChainID
	if rule.Protocol == model.RuleProtocolTransparent || rule.UseClashRuleset {
		var err error
		chainID, err = s.prepareTransparentChain(client, tunnel, rule, mark)
		if err != nil {
			return err
		}
	}
	if targets == nil {
		targets = rule.Targets
	}
	return s.provisionService(client, node, rule, chainID, targets, mark)
}

// Stop 停止规则
func (s *RuleService) Stop(id uint, username, ip, userAgent string) error {
	rule, err := s.ruleRepo.FindByID(id)
	if err != nil {
		return err
	}
	if err := s.requireUnlinkedEntrance(rule); err != nil {
		return err
	}

	if rule.Status != model.RuleStatusRunning && rule.ServiceID == "" {
		return nil
	}

	if err := s.stopSelectedEntrance(rule); err != nil {
		return err
	}

	s.logService.Record(
		username,
		model.ActionStop,
		model.ResourceTypeRule,
		id,
		fmt.Sprintf("停止规则: %s", rule.Name),
		ip,
		userAgent)

	logger.Infof("停止规则成功: %s", rule.Name)
	return nil
}

// stopSelectedEntrance is used by the owning tunnel to stop its ingress service.
func (s *RuleService) stopSelectedEntrance(rule *model.GostRule) error {
	if rule.ServiceID != "" {
		node, err := s.nodeRepo.FindByID(ruleEntryNodeID(rule))
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return errors.ErrNodeNotFound
		}
		if err != nil {
			return err
		}
		if node.Status != model.NodeStatusOnline {
			return errors.ErrNodeOffline
		}
		client := utils.GetGostClient(node)
		if err := client.DeleteService(rule.ServiceID); err != nil {
			return err
		}
		if err := client.SaveConfig(); err != nil {
			return err
		}
	}
	// Retain the service ID until the node config and local state are both saved;
	// a database write failure must leave enough information to retry the stop.
	if err := s.ruleRepo.UpdateRuntime(rule.ID, "", model.RuleStatusStopped); err != nil {
		return err
	}
	rule.ServiceID = ""
	rule.Status = model.RuleStatusStopped
	return nil
}

func ruleEntryNodeID(rule *model.GostRule) uint {
	if rule.Type == model.RuleTypeTunnel && rule.Tunnel != nil {
		return rule.Tunnel.EntryNodeID
	}
	if rule.NodeID != nil {
		return *rule.NodeID
	}
	return 0
}

func (s *RuleService) setupRuleObserver(client *gost.Client, rule *model.GostRule, svc *gost.ServiceConfig) error {
	if config.Get().Report.Listen == "" {
		return errors.ErrReportNotConfigured
	}
	address, err := client.ObserverAddress()
	if err != nil {
		return errors.ErrObserverCreateFailed
	}
	const observerName = "observer-global"
	if err := client.SetObserver(&gost.ObserverConfig{
		Name: observerName,
		Plugin: &gost.PluginConfig{
			Type:  "grpc",
			Addr:  address,
			Token: client.ObserverToken(),
		},
	}); err != nil {
		logger.Warnf("配置节点观察器失败: %v", err)
		return errors.ErrObserverCreateFailed
	}
	if err := s.ruleRepo.UpdateObserverID(rule.ID, observerName); err != nil {
		return err
	}
	svc.Observer = observerName
	if svc.Metadata == nil {
		svc.Metadata = make(map[string]any)
	}
	svc.Metadata["enableStats"] = true
	svc.Metadata["observer.period"] = "5s"
	// Gost also reads these counters when serving GET /config.
	svc.Metadata["observer.resetTraffic"] = false
	return nil
}

// buildAndStartService 构建并启动 Gost 服务 (处理通用逻辑)
func (s *RuleService) buildAndStartService(client *gost.Client, node *model.GostNode, rule *model.GostRule, chainID string, targets []string) error {
	if err := s.provisionService(client, node, rule, chainID, targets, gost.ClashRedirectMark); err != nil {
		return err
	}
	return s.persistStartedService(client, rule, rule.Type == model.RuleTypeForward && chainID != "")
}

func entranceServiceName(id uint) string {
	return fmt.Sprintf("rule-%d", id)
}

func (s *RuleService) provisionService(client *gost.Client, node *model.GostNode, rule *model.GostRule, chainID string, targets []string, mark uint32) error {
	serviceName := entranceServiceName(rule.ID)
	strategy := rule.Strategy
	if strategy == "" || len(targets) == 1 {
		strategy = "round"
	}

	var svc *gost.ServiceConfig
	if rule.Protocol == model.RuleProtocolTransparent || rule.UseClashRuleset {
		svc = gost.BuildMarkedTransparentRedirectService(serviceName, mark, transparentFiltering(rule))
	} else if rule.Protocol == model.RuleProtocolTCP {
		svc = gost.BuildTCPForwardService(serviceName, rule.ListenPort, targets, strategy)
	} else {
		svc = gost.BuildUDPForwardService(serviceName, rule.ListenPort, targets, strategy)
	}
	svc.Addr = net.JoinHostPort(rule.ListenIP, strconv.Itoa(rule.ListenPort))

	// 如果有 Chain ID，则关联（用于隧道转发）
	if chainID != "" {
		svc.Handler.Chain = chainID
	}

	// 配置观察器
	if err := s.setupRuleObserver(client, rule, svc); err != nil {
		return err
	}

	if _, err := createServiceWithTrafficQuota(s.nodeRepo, client, *node, svc); err != nil {
		_ = s.ruleRepo.UpdateStatus(rule.ID, model.RuleStatusError)
		if stderrors.Is(err, errors.ErrNodeTrafficQuotaUnavailable) {
			return err
		}
		return errors.ErrRuleStartFailed
	}

	rule.ServiceID = serviceName
	return nil
}

func (s *RuleService) persistStartedService(client *gost.Client, rule *model.GostRule, selected bool) error {
	serviceName := rule.ServiceID
	// The tunnel rolls back the live service if its configuration cannot be saved.
	if err := client.SaveConfig(); err != nil && selected {
		// Retain the live listener identity if rollback also fails and cleanup is retried after reload.
		_ = s.ruleRepo.UpdateRuntime(rule.ID, serviceName, model.RuleStatusError)
		return errors.ErrRuleStartFailed
	}
	if err := s.ruleRepo.UpdateRuntime(rule.ID, serviceName, model.RuleStatusRunning); err != nil {
		if !selected {
			_ = client.DeleteService(serviceName)
			_ = client.SaveConfig()
		}
		return err
	}
	rule.Status = model.RuleStatusRunning

	return nil
}
