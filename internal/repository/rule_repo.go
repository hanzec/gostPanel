package repository

import (
	"errors"
	"math"

	"yagp/internal/model"

	"gorm.io/gorm"
)

var ErrTrafficCounterOverflow = errors.New("traffic counter limit exceeded")

// RuleRepository 规则仓库
type RuleRepository struct {
	*BaseRepository
}

// NewRuleRepository 创建规则仓库
func NewRuleRepository(db *gorm.DB) *RuleRepository {
	return &RuleRepository{
		BaseRepository: NewBaseRepository(db),
	}
}

// Create 创建规则
func (r *RuleRepository) Create(rule *model.GostRule) error {
	return r.DB.Create(rule).Error
}

// Update 更新规则
func (r *RuleRepository) Update(rule *model.GostRule) error {
	return r.DB.Save(rule).Error
}

// Delete 删除规则
func (r *RuleRepository) Delete(id uint) error {
	return r.DB.Delete(&model.GostRule{}, id).Error
}

// FindByID 根据 ID 查询规则
func (r *RuleRepository) FindByID(id uint) (*model.GostRule, error) {
	var rule model.GostRule
	err := r.DB.Preload("Node").Preload("Tunnel").Preload("Tunnel.EntryNode").Preload("Tunnel.ExitNode").First(&rule, id).Error
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

// List 查询规则列表
func (r *RuleRepository) List(opt *QueryOption) ([]model.GostRule, int64, error) {
	var rules []model.GostRule
	var total int64

	db := r.DB.Model(&model.GostRule{})

	// 应用条件过滤
	db = ApplyConditions(db, opt)

	// 统计总数（包含过滤条件）
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 预加载节点和隧道
	db = db.Preload("Node").Preload("Tunnel").Preload("Tunnel.EntryNode").Preload("Tunnel.ExitNode")

	// 默认按创建时间倒序
	if opt == nil || len(opt.Orders) == 0 {
		db = db.Order("created_at DESC")
	}

	// 应用分页
	db = ApplyPagination(db, opt)

	if err := db.Find(&rules).Error; err != nil {
		return nil, 0, err
	}

	return rules, total, nil
}

// FindByNodeID 根据节点 ID 查询规则
func (r *RuleRepository) FindByNodeID(nodeID uint) ([]model.GostRule, error) {
	var rules []model.GostRule
	err := r.DB.Where("node_id = ?", nodeID).Find(&rules).Error
	return rules, err
}

// FindByTunnelID 根据隧道 ID 查询规则
func (r *RuleRepository) FindByTunnelID(tunnelID uint) ([]model.GostRule, error) {
	var rules []model.GostRule
	err := r.DB.Where("tunnel_id = ?", tunnelID).Find(&rules).Error
	return rules, err
}

// EntranceBindings finds the tunnels selecting any of the listed rule IDs.
func (r *RuleRepository) EntranceBindings(ruleIDs []uint) ([]model.GostTunnel, error) {
	var tunnels []model.GostTunnel
	err := r.DB.Select("id", "entry_rule_id", "exit_rule_id", "status").
		Where("entry_rule_id IN ? OR exit_rule_id IN ?", ruleIDs, ruleIDs).Find(&tunnels).Error
	return tunnels, err
}

// ListenerConflicts checks whether a rule on the same ingress overlaps this bind.
func (r *RuleRepository) ListenerConflicts(nodeID uint, protocol model.RuleProtocol, ip string, port int, excludeID ...uint) (bool, error) {
	var count int64
	tunnels := r.DB.Model(&model.GostTunnel{}).Select("id").Where("entry_node_id = ?", nodeID)
	protocols := []string{"tcp", "tls", "mtls", "ws", "mws"}
	if protocol.ListenerNetwork() == "udp" {
		protocols = []string{"udp", "quic"}
	}
	db := r.DB.Model(&model.GostRule{}).
		Where("protocol IN ? AND listen_port = ?", protocols, port).
		Where("((type = ? AND node_id = ?) OR (type = ? AND tunnel_id IN (?)))",
			model.RuleTypeForward, nodeID, model.RuleTypeTunnel, tunnels)
	if ip != "" {
		db = db.Where("listen_ip IN ?", []string{"", ip})
	}
	if len(excludeID) > 0 {
		db = db.Where("id != ?", excludeID[0])
	}
	err := db.Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateStatus 更新规则状态
func (r *RuleRepository) UpdateStatus(id uint, status model.RuleStatus) error {
	return r.UpdateField(&model.GostRule{}, id, "status", status)
}

// UpdateServiceID 更新服务 ID
func (r *RuleRepository) UpdateServiceID(id uint, serviceID string) error {
	return r.UpdateField(&model.GostRule{}, id, "service_id", serviceID)
}

// UpdateRuntime updates the service ID and status together.
func (r *RuleRepository) UpdateRuntime(id uint, serviceID string, status model.RuleStatus) error {
	return r.UpdateFields(&model.GostRule{}, id, map[string]interface{}{
		"service_id": serviceID,
		"status":     status,
	})
}

// UpdateObserverID 更新观察器 ID
func (r *RuleRepository) UpdateObserverID(id uint, observerID string) error {
	return r.UpdateField(&model.GostRule{}, id, "observer_id", observerID)
}

// CountByNodeID 按节点统计数量
func (r *RuleRepository) CountByNodeID(nodeID uint) (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostRule{}).Where("node_id = ?", nodeID).Count(&count).Error
	return count, err
}

// CountAll 统计总数
func (r *RuleRepository) CountAll() (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostRule{}).Count(&count).Error
	return count, err
}

// CountByStatus 按状态统计
func (r *RuleRepository) CountByStatus(status model.RuleStatus) (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostRule{}).Where("status = ?", status).Count(&count).Error
	return count, err
}

// CountByType 按类型统计
func (r *RuleRepository) CountByType(ruleType model.RuleType) (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostRule{}).Where("type = ?", ruleType).Count(&count).Error
	return count, err
}

// StopByNodeID 停止指定节点的所有规则
func (r *RuleRepository) StopByNodeID(nodeID uint) error {
	return r.DB.Model(&model.GostRule{}).
		Where("node_id = ? AND status = ?", nodeID, model.RuleStatusRunning).
		Update("status", model.RuleStatusStopped).Error
}

// UpdateTrafficSample atomically stores the observer checkpoint and increments rule totals.
func (r *RuleRepository) UpdateTrafficSample(id uint, delta, observed model.ObserverCounters) error {
	// Guard the addition in SQL so an overflowing report cannot corrupt SQLite's integer columns.
	result := r.DB.Model(&model.GostRule{}).Where("id = ?", id).
		Where("input_bytes <= ? AND output_bytes <= ? AND total_bytes <= ? AND total_requests <= ?",
			math.MaxInt64-delta.InputBytes, math.MaxInt64-delta.OutputBytes,
			math.MaxInt64-delta.InputBytes-delta.OutputBytes, math.MaxInt64-delta.Connections).
		Updates(map[string]interface{}{
			"input_bytes":           gorm.Expr("input_bytes + ?", delta.InputBytes),
			"output_bytes":          gorm.Expr("output_bytes + ?", delta.OutputBytes),
			"total_bytes":           gorm.Expr("total_bytes + ?", delta.InputBytes+delta.OutputBytes),
			"total_requests":        gorm.Expr("total_requests + ?", delta.Connections),
			"observer_input_bytes":  observed.InputBytes,
			"observer_output_bytes": observed.OutputBytes,
			"observer_connections":  observed.Connections,
		})
	// MySQL can report zero changed rows for an unchanged snapshot; only a positive delta can overflow.
	if result.Error == nil && result.RowsAffected == 0 && (delta.InputBytes != 0 || delta.OutputBytes != 0 || delta.Connections != 0) {
		return ErrTrafficCounterOverflow
	}
	return result.Error
}

// StopByTunnelIDs 停止指定隧道列表关联的所有规则
func (r *RuleRepository) StopByTunnelIDs(tunnelIDs []uint) error {
	if len(tunnelIDs) == 0 {
		return nil
	}
	return r.DB.Model(&model.GostRule{}).
		Where("tunnel_id IN ? AND status = ?", tunnelIDs, model.RuleStatusRunning).
		Update("status", model.RuleStatusStopped).Error
}
