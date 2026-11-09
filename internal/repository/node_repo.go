package repository

import (
	"context"
	"time"
	"yagp/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NodeRepository 节点仓库
type NodeRepository struct {
	*BaseRepository
}

// NewNodeRepository 创建节点仓库
func NewNodeRepository(db *gorm.DB) *NodeRepository {
	return &NodeRepository{
		BaseRepository: NewBaseRepository(db),
	}
}

// Create 创建节点
func (r *NodeRepository) Create(node *model.GostNode) error {
	return r.DB.Create(node).Error
}

// Update 更新节点元数据，不覆盖并发完成的注册或健康检查。
func (r *NodeRepository) Update(node *model.GostNode, trafficLimitBytes *int64, trafficResetPeriod *string) error {
	fields := []string{"Name", "Remark"}
	limitChanged := trafficLimitBytes != nil && node.TrafficLimitBytes != *trafficLimitBytes
	periodChanged := trafficResetPeriod != nil && node.TrafficResetPeriod != *trafficResetPeriod
	if limitChanged {
		node.TrafficLimitBytes = *trafficLimitBytes
		fields = append(fields, "TrafficLimitBytes")
	}
	if periodChanged {
		node.TrafficResetPeriod = *trafficResetPeriod
		fields = append(fields, "TrafficResetPeriod")
	}
	if limitChanged || (periodChanged && (node.TrafficLimitBytes > 0 || node.TrafficQuotaState != "")) {
		node.TrafficQuotaState = model.TrafficQuotaPending
		fields = append(fields, "TrafficQuotaState")
	}
	return r.DB.Model(node).Select(fields).Updates(node).Error
}

// UpdateTrafficQuota stores the daemon snapshot for the policy that was applied.
func (r *NodeRepository) UpdateTrafficQuota(node model.GostNode, window int64, used uint64, state string) error {
	if node.TrafficQuotaWindowStart == window && node.TrafficQuotaUsedBytes == used && node.TrafficQuotaState == state {
		return nil
	}
	return r.DB.Model(&model.GostNode{}).
		Where("id = ? AND traffic_limit_bytes = ? AND traffic_reset_period = ?", node.ID, node.TrafficLimitBytes, node.TrafficResetPeriod).
		Updates(map[string]any{
			"traffic_quota_window_start": window,
			"traffic_quota_used_bytes":   used,
			"traffic_quota_state":        state,
		}).Error
}

func (r *NodeRepository) SetRegistrationToken(ctx context.Context, id uint, hash string, expiresAt time.Time) error {
	result := r.DB.WithContext(ctx).Model(&model.GostNode{}).Where("id = ?", id).Updates(map[string]any{
		"registration_token_hash": hash,
		"registration_expires_at": expiresAt,
	})
	if result.Error == nil && result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return result.Error
}

func (r *NodeRepository) EnrollNode(ctx context.Context, node *model.GostNode, hash string, credentials model.NodeCredentials) error {
	// Do not interpolate node secrets or their hashes into GORM's SQL trace.
	return r.DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		// Existing Gost services and observers already use these local credentials.
		values := nodeCredentialValues(credentials)
		values["registration_token_hash"] = ""
		values["registration_expires_at"] = nil
		values["username"] = gorm.Expr("COALESCE(NULLIF(username, ''), ?)", node.Username)
		values["password"] = gorm.Expr("COALESCE(NULLIF(password, ''), ?)", node.Password)
		values["address"] = node.Address
		values["ipv4"] = node.IPv4
		values["ipv6"] = node.IPv6
		values["region"] = ""
		values["session_generation"] = gorm.Expr("session_generation + 1")
		result := tx.Model(&model.GostNode{}).
			Where("id = ? AND registration_token_hash = ? AND registration_expires_at > ?", node.ID, hash, time.Now().UTC()).
			Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.First(node, node.ID).Error
	})
}

func (r *NodeRepository) UpdateIPs(ctx context.Context, node *model.GostNode, ipv4, ipv6 string) error {
	// The session keeps its authenticated generation, but the addresses can change while it is connected.
	query := r.DB.WithContext(ctx).Model(&model.GostNode{}).
		Where("id = ? AND session_generation = ?", node.ID, node.SessionGeneration)
	var current model.GostNode
	if err := query.Session(&gorm.Session{}).Select("ipv4", "ipv6").Take(&current).Error; err != nil {
		return err
	}
	updates := make(map[string]any)
	if ipv6 != "" && ipv6 != current.IPv6 {
		updates["ipv6"] = ipv6
		updates["address"] = gorm.Expr("COALESCE(NULLIF(ipv4, ''), ?)", ipv6)
	}
	if ipv4 != "" && ipv4 != current.IPv4 {
		updates["ipv4"] = ipv4
		updates["address"] = ipv4
	}
	if len(updates) == 0 {
		return nil
	}
	updates["region"] = ""
	// Keep the generation predicate on the write if the node re-enrolls concurrently.
	result := query.Updates(updates)
	if result.Error == nil && result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return result.Error
}

// Delete 删除节点
func (r *NodeRepository) Delete(id uint) error {
	return r.DB.Delete(&model.GostNode{}, id).Error
}

// FindByID 根据 ID 查询节点
func (r *NodeRepository) FindByID(id uint) (*model.GostNode, error) {
	var node model.GostNode
	err := r.DB.First(&node, id).Error
	if err != nil {
		return nil, err
	}
	return &node, nil
}

// FindByIDWithRelations 根据 ID 查询节点（包含关联）
func (r *NodeRepository) FindByIDWithRelations(id uint) (*model.GostNode, error) {
	var node model.GostNode
	err := r.DB.Preload("Rules").Preload("EntryTunnels").Preload("ExitTunnels").First(&node, id).Error
	if err != nil {
		return nil, err
	}
	return &node, nil
}

// List 查询节点列表
func (r *NodeRepository) List(opt *QueryOption) ([]model.GostNode, int64, error) {
	var nodes []model.GostNode
	var total int64

	db := r.DB.Model(&model.GostNode{})

	// 应用条件过滤（统计总数前）
	db = ApplyConditions(db, opt)

	// 统计总数（包含过滤条件）
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 默认按创建时间倒序
	if opt == nil || len(opt.Orders) == 0 {
		db = db.Order("created_at DESC")
	}

	// 应用分页
	db = ApplyPagination(db, opt)

	if err := db.Find(&nodes).Error; err != nil {
		return nil, 0, err
	}

	return nodes, total, nil
}

// FindByName 根据名称查询节点
func (r *NodeRepository) FindByName(name string) (*model.GostNode, error) {
	var node model.GostNode
	err := r.DB.Where("name = ?", name).First(&node).Error
	if err != nil {
		return nil, err
	}
	return &node, nil
}

// ExistsByName 检查名称是否存在
func (r *NodeRepository) ExistsByName(name string, excludeID ...uint) (bool, error) {
	var count int64
	db := r.DB.Model(&model.GostNode{}).Where("name = ?", name)
	if len(excludeID) > 0 {
		db = db.Where("id != ?", excludeID[0])
	}
	err := db.Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateStatus 更新节点状态
func (r *NodeRepository) UpdateStatus(id uint, status model.NodeStatus) error {
	return r.UpdateField(&model.GostNode{}, id, "status", status)
}

// UpdateLastCheck 更新最后检查时间
func (r *NodeRepository) UpdateLastCheck(id uint) error {
	return r.UpdateField(&model.GostNode{}, id, "last_check_at", time.Now())
}

// GetAllOnline 获取所有在线节点
func (r *NodeRepository) GetAllOnline() ([]model.GostNode, error) {
	var nodes []model.GostNode
	err := r.DB.Where("status = ?", model.NodeStatusOnline).Find(&nodes).Error
	return nodes, err
}

// CountByStatus 按状态统计数量
func (r *NodeRepository) CountByStatus(status model.NodeStatus) (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostNode{}).Where("status = ?", status).Count(&count).Error
	return count, err
}

// CountAll 统计总数
func (r *NodeRepository) CountAll() (int64, error) {
	var count int64
	err := r.DB.Model(&model.GostNode{}).Count(&count).Error
	return count, err
}

// UpdateStats 累加节点流量
func (r *NodeRepository) UpdateStats(id uint, inputBytes, outputBytes int64) error {
	return r.DB.Model(&model.GostNode{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"input_bytes":  gorm.Expr("input_bytes + ?", inputBytes),
			"output_bytes": gorm.Expr("output_bytes + ?", outputBytes),
			"total_bytes":  gorm.Expr("total_bytes + ?", inputBytes+outputBytes),
		}).Error
}
