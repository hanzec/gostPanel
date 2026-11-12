// Package service 提供业务逻辑层服务
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"slices"
	"time"

	"yagp/internal/dto"
	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"

	"gorm.io/gorm"
)

const registrationTokenLifetime = 15 * time.Minute

// NodeService 节点服务
// 负责节点的 CRUD 操作和业务逻辑处理
type NodeService struct {
	nodeRepo                *repository.NodeRepository
	logService              *LogService
	registrationFingerprint string
}

// NewNodeService 创建节点服务
func NewNodeService(db *gorm.DB, registrationFingerprint string) *NodeService {
	return &NodeService{
		nodeRepo:                repository.NewNodeRepository(db),
		logService:              NewLogService(db),
		registrationFingerprint: registrationFingerprint,
	}
}

// Create 创建节点
// 创建前会检查节点名称是否已存在
func (s *NodeService) Create(req *dto.CreateNodeReq, username, ip, userAgent string) (*model.GostNode, error) {
	if len(req.AddressMappings) > 0 {
		return nil, errors.ErrNodeMappingListenIP
	}
	// 检查名称是否存在
	exists, err := s.nodeRepo.ExistsByName(req.Name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.ErrNodeNameExists
	}

	// 创建节点
	node := &model.GostNode{
		Name:               req.Name,
		Remark:             req.Remark,
		Status:             model.NodeStatusOffline,
		TrafficResetPeriod: model.TrafficResetNone,
	}
	if req.TrafficLimitBytes != nil {
		node.TrafficLimitBytes = *req.TrafficLimitBytes
	}
	if req.TrafficResetPeriod != nil {
		node.TrafficResetPeriod = *req.TrafficResetPeriod
	}
	if node.TrafficLimitBytes > 0 {
		node.TrafficQuotaState = model.TrafficQuotaPending
	}

	if err = s.nodeRepo.Create(node); err != nil {
		return nil, err
	}

	// 记录操作日志
	s.logService.Record(
		username,
		model.ActionCreate,
		model.ResourceTypeNode,
		node.ID,
		fmt.Sprintf("创建节点: %s", node.Name),
		ip,
		userAgent)

	logger.Infof("创建节点成功: %s", node.Name)
	return node, nil
}

// Update 更新节点
// 更新前会检查节点是否存在以及新名称是否与其他节点冲突
func (s *NodeService) Update(id uint, req *dto.UpdateNodeReq, username, ip, userAgent string) (*model.GostNode, error) {
	// 查询节点
	node, err := s.nodeRepo.FindByID(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrNodeNotFound
		}
		return nil, err
	}

	// 检查名称是否存在（排除自身）
	exists, err := s.nodeRepo.ExistsByName(req.Name, id)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.ErrNodeNameExists
	}

	if err = validateNodeAddressMappings(node, req.AddressMappings); err != nil {
		return nil, err
	}

	// 更新节点
	node.Name = req.Name
	node.Remark = req.Remark

	if err = s.nodeRepo.Update(node, req.TrafficLimitBytes, req.TrafficResetPeriod, req.AddressMappings); err != nil {
		return nil, err
	}

	// 记录操作日志
	s.logService.Record(
		username,
		model.ActionUpdate,
		model.ResourceTypeNode,
		node.ID,
		fmt.Sprintf("更新节点: %s", node.Name),
		ip,
		userAgent)

	return node, nil
}

func validateNodeAddressMappings(node *model.GostNode, mappings []model.NodeAddressMapping) error {
	available := node.LocalAddresses
	if available == nil {
		available = node.Addresses
		if len(available) == 0 {
			available = []string{node.IPv4, node.IPv6}
		}
	}
	for _, mapping := range mappings {
		if slices.Contains(available, mapping.ListenIP) {
			continue
		}
		// A refreshed address list may omit a listener that was already configured.
		if !slices.ContainsFunc(node.AddressMappings, func(existing model.NodeAddressMapping) bool {
			return existing.ListenIP == mapping.ListenIP
		}) {
			return errors.ErrNodeMappingListenIP
		}
	}
	return nil
}

func (s *NodeService) IssueRegistrationToken(ctx context.Context, id uint, username, ip, userAgent string) (*dto.NodeRegistrationToken, error) {
	issued := &dto.NodeRegistrationToken{
		Token:     fmt.Sprintf("%d.%s", id, newNodeSecret()),
		ExpiresAt: time.Now().UTC().Add(registrationTokenLifetime),
	}
	if s.registrationFingerprint != "" {
		issued.Token += "." + s.registrationFingerprint
	}
	if err := s.nodeRepo.SetRegistrationToken(ctx, id, registrationTokenHash(issued.Token), issued.ExpiresAt); err != nil {
		if !stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if _, err := s.GetByID(id); err != nil {
			return nil, err
		}
		return nil, errors.ErrNodeOnline
	}
	s.logService.Record(username, model.ActionUpdate, model.ResourceTypeNode, id, "签发节点注册 token", ip, userAgent)
	return issued, nil
}

func newNodeSecret() string {
	var secret [32]byte
	rand.Read(secret[:])
	return base64.RawURLEncoding.EncodeToString(secret[:])
}

func registrationTokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (s *NodeService) UpdateIPs(ctx context.Context, node *model.GostNode, ipv4, ipv6 string, addresses, localAddresses []string, mptcpSupported bool) error {
	return s.nodeRepo.UpdateIPs(ctx, node, ipv4, ipv6, addresses, localAddresses, mptcpSupported)
}

func (s *NodeService) RevokeCredentials(ctx context.Context, id uint) error {
	if err := s.nodeRepo.RevokeNodeCredentials(ctx, id); err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return errors.ErrNodeNotFound
		}
		return err
	}
	gost.DisconnectNode(id)
	return nil
}

// Delete 删除节点
// 删除前会检查节点是否存在以及是否有关联的转发规则
func (s *NodeService) Delete(id uint, username, ip, userAgent string) error {
	// 查询节点（包含关联）
	node, err := s.nodeRepo.FindByIDWithRelations(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return errors.ErrNodeNotFound
		}
		return err
	}

	// 检查是否有关联的规则
	if len(node.Rules) > 0 {
		return errors.ErrNodeHasRules
	}

	// 删除节点前，用户需要手动删除相关隧道
	if len(node.EntryTunnels) > 0 || len(node.ExitTunnels) > 0 {
		return errors.ErrNodeHasTunnels
	}

	// 删除节点
	if err = s.nodeRepo.Delete(id); err != nil {
		return err
	}
	gost.DisconnectNode(id)

	// 记录操作日志
	s.logService.Record(
		username,
		model.ActionDelete,
		model.ResourceTypeNode,
		id,
		fmt.Sprintf("删除节点: %s", node.Name),
		ip,
		userAgent)

	logger.Infof("删除节点成功: %s", node.Name)
	return nil
}

// GetByID 获取节点详情
func (s *NodeService) GetByID(id uint) (*model.GostNode, error) {
	node, err := s.nodeRepo.FindByID(id)
	if err != nil {
		if stderrors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.ErrNodeNotFound
		}
		return nil, err
	}
	return node, nil
}

// List 获取节点列表
func (s *NodeService) List(req *dto.NodeListReq) ([]model.GostNode, int64, error) {
	// 设置默认值
	req.SetDefaults()

	opt := &repository.QueryOption{
		Pagination: &repository.Pagination{
			Page:     req.Page,
			PageSize: req.PageSize,
		},
		Conditions: make(map[string]any),
	}

	// 状态筛选
	if req.Status != "" {
		opt.Conditions["status = ?"] = req.Status
	}

	// 关键词搜索
	if req.Keyword != "" {
		keyword := "%" + req.Keyword + "%"
		opt.Conditions["name LIKE ? OR address LIKE ? OR ipv4 LIKE ? OR ipv6 LIKE ? OR addresses LIKE ? OR local_addresses LIKE ? OR address_mappings LIKE ?"] = []any{keyword, keyword, keyword, keyword, keyword, keyword, keyword}
	}

	return s.nodeRepo.List(opt)
}

// CreateGostClient 创建节点的 Gost 客户端
func (s *NodeService) CreateGostClient(id uint) (*gost.Client, error) {
	node, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	return utils.GetGostClient(node), nil
}

func (s *NodeService) RecordOperation(id uint, details, username, ip, userAgent string) {
	s.logService.Record(username, model.ActionUpdate, model.ResourceTypeNode, id, details, ip, userAgent)
}

// GetConfig 获取节点配置
func (s *NodeService) GetConfig(id uint) (*gost.GostConfig, error) {
	node, err := s.nodeRepo.FindByID(id)
	if err != nil {
		return nil, err
	}

	client := utils.GetGostClient(node)

	config, err := client.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("获取节点配置失败: %v", err)
	}

	return config, nil
}
