package repository

import (
	"context"

	"yagp/internal/model"

	"gorm.io/gorm"
)

// TunnelRepository 隧道仓库
type TunnelRepository struct {
	*BaseRepository
}

// NewTunnelRepository 创建隧道仓库
func NewTunnelRepository(db *gorm.DB) *TunnelRepository {
	return &TunnelRepository{
		BaseRepository: NewBaseRepository(db),
	}
}

// Create 创建隧道
func (r *TunnelRepository) Create(tunnel *model.GostTunnel) error {
	return r.DB.Create(tunnel).Error
}

// Update 只更新隧道可编辑的配置。
func (r *TunnelRepository) Update(tunnel *model.GostTunnel, updateAllowlist bool) error {
	fields := []string{"name", "entry_node_id", "exit_node_id", "entry_rule_id", "exit_rule_id", "protocol", "relay_port", "remark", "updated_at"}
	if updateAllowlist {
		fields = append(fields, "allowlist_node_ids")
	}
	return r.DB.Model(tunnel).Select(fields).Updates(tunnel).Error
}

func (r *TunnelRepository) UpdateAllowlist(ctx context.Context, tunnel *model.GostTunnel) error {
	return r.DB.WithContext(ctx).Model(tunnel).Select("allowlist_node_ids", "updated_at").Updates(tunnel).Error
}

// Delete 删除隧道
func (r *TunnelRepository) Delete(id uint) error {
	return r.DB.Delete(&model.GostTunnel{}, id).Error
}

// FindByID 根据 ID 查询隧道（包含关联节点）
func (r *TunnelRepository) FindByID(id uint) (*model.GostTunnel, error) {
	var tunnel model.GostTunnel
	err := r.DB.Preload("EntryNode").Preload("ExitNode").Preload("EntryRule").Preload("ExitRule").First(&tunnel, id).Error
	if err != nil {
		return nil, err
	}
	return &tunnel, nil
}

// FindByEntranceID looks up the tunnel reserving either role of an entrance.
func (r *TunnelRepository) FindByEntranceID(ruleID uint) (*model.GostTunnel, error) {
	var tunnel model.GostTunnel
	err := r.DB.Where("entry_rule_id = ? OR exit_rule_id = ?", ruleID, ruleID).First(&tunnel).Error
	return &tunnel, err
}

// List 查询隧道列表
func (r *TunnelRepository) List(opt *QueryOption) ([]model.GostTunnel, int64, error) {
	var tunnels []model.GostTunnel
	var total int64

	db := r.DB.Model(&model.GostTunnel{})

	// 应用条件过滤
	db = ApplyConditions(db, opt)

	// 统计总数（包含过滤条件）
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 预加载节点和关联规则
	db = db.Preload("EntryNode").Preload("ExitNode").Preload("EntryRule").Preload("ExitRule")

	// 默认按创建时间倒序
	if opt == nil || len(opt.Orders) == 0 {
		db = db.Order("created_at DESC")
	}

	// 应用分页
	db = ApplyPagination(db, opt)

	if err := db.Find(&tunnels).Error; err != nil {
		return nil, 0, err
	}

	return tunnels, total, nil
}

// UpdateStatus 更新隧道状态
func (r *TunnelRepository) UpdateStatus(id uint, status model.TunnelStatus) error {
	return r.UpdateField(&model.GostTunnel{}, id, "status", status)
}

// CountAll 统计总数
func (r *TunnelRepository) CountAll() (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostTunnel{}).Count(&count).Error
	return count, err
}

// FindByNodeID 查找节点相关的隧道
func (r *TunnelRepository) FindByNodeID(nodeID uint) ([]model.GostTunnel, error) {
	var tunnels []model.GostTunnel
	err := r.DB.Where("entry_node_id = ? OR exit_node_id = ?", nodeID, nodeID).Preload("ExitRule").Find(&tunnels).Error
	return tunnels, err
}

// StopByNodeID clears legacy status; endpoint tunnels keep their applied config while control is offline.
func (r *TunnelRepository) StopByNodeID(nodeID uint) error {
	return r.DB.Model(&model.GostTunnel{}).
		Where("(entry_node_id = ? OR exit_node_id = ?) AND status = ? AND entry_rule_id IS NULL", nodeID, nodeID, model.TunnelStatusRunning).
		Update("status", model.TunnelStatusStopped).Error
}

// HasRulesUsingTunnel 检查是否有规则正在使用该隧道
func (r *TunnelRepository) HasRulesUsingTunnel(tunnelID uint) (bool, error) {
	var count int64
	err := r.DB.Model(&model.GostRule{}).Where("tunnel_id = ?", tunnelID).Count(&count).Error
	return count > 0, err
}

// HasRules 检查隧道是否被规则使用（别名方法）
func (r *TunnelRepository) HasRules(tunnelID uint) (bool, error) {
	return r.HasRulesUsingTunnel(tunnelID)
}

// CountByStatus 按状态统计
func (r *TunnelRepository) CountByStatus(status model.TunnelStatus) (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostTunnel{}).Where("status = ?", status).Count(&count).Error
	return count, err
}

// UpdateServiceInfo 更新隧道的服务 ID 和 Chain ID
func (r *TunnelRepository) UpdateServiceInfo(id uint, serviceID, chainID string) error {
	return r.DB.Model(&model.GostTunnel{}).Where("id = ?", id).
		Updates(map[string]any{
			"service_id": serviceID,
			"chain_id":   chainID,
		}).Error
}
