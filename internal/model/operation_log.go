package model

import (
	"time"
)

// OperationLog 操作日志模型
type OperationLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"type:text" json:"username"`      // Authentik 用户名
	Action       string    `gorm:"size:50;not null" json:"action"` // 操作类型
	ResourceType string    `gorm:"size:50" json:"resource_type"`   // 资源类型
	ResourceID   uint      `json:"resource_id"`                    // 资源ID
	Details      string    `gorm:"type:text" json:"details"`       // 详细信息 (JSON)
	IPAddress    string    `gorm:"size:50" json:"ip_address"`      // IP 地址
	UserAgent    string    `gorm:"size:255" json:"user_agent"`     // User-Agent
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

// TableName 指定表名
func (OperationLog) TableName() string {
	return "operation_logs"
}

// 操作类型常量
const (
	ActionCreate = "create" // 创建
	ActionUpdate = "update" // 更新
	ActionDelete = "delete" // 删除
	ActionStart  = "start"  // 启动
	ActionStop   = "stop"   // 停止
)

// 资源类型常量
const (
	ResourceTypeNode   = "node"   // 节点
	ResourceTypeRule   = "rule"   // 规则
	ResourceTypeTunnel = "tunnel" // 隧道
)
