package model

import (
	"time"

	"gorm.io/gorm"
)

// NodeStatus 节点状态
type NodeStatus string

const (
	NodeStatusOnline  NodeStatus = "online"  // 在线
	NodeStatusOffline NodeStatus = "offline" // 离线
	NodeStatusError   NodeStatus = "error"   // 错误
)

const (
	TrafficResetNone        = "none"
	TrafficQuotaActive      = "active"
	TrafficQuotaBlocked     = "blocked"
	TrafficQuotaPending     = "pending"
	TrafficQuotaUnsupported = "unsupported"
	TrafficQuotaWindowUnset = -1
)

var trafficQuotaLocation = time.FixedZone("UTC+8", 8*60*60)

type NodeUpgradePolicy struct {
	AutoUpgradeGost   bool `gorm:"not null;default:false" json:"auto_upgrade_gost"`
	AutoUpgradeDaemon bool `gorm:"not null;default:false" json:"auto_upgrade_daemon"`
}

// GostNode Gost 节点模型
type GostNode struct {
	ID       uint       `gorm:"primaryKey" json:"id"`
	Name     string     `gorm:"size:100;not null" json:"name"`         // 节点名称
	Address  string     `gorm:"size:255;not null" json:"address"`      // 转发地址，优先使用 IPv4
	Port     int        `gorm:"not null" json:"-"`                     // 保留已部署数据库中的非空列
	Username string     `gorm:"size:50" json:"username"`               // API 认证用户名
	Password string     `gorm:"size:255" json:"-"`                     // API 认证密码
	Status   NodeStatus `gorm:"size:20;default:offline" json:"status"` // 状态
	IPv4     string     `gorm:"column:ipv4;size:45" json:"ipv4"`
	IPv6     string     `gorm:"column:ipv6;size:45" json:"ipv6"`

	RegistrationTokenHash string            `gorm:"size:64" json:"-"`
	RegistrationExpiresAt *time.Time        `json:"-"`
	UpgradePolicy         NodeUpgradePolicy `gorm:"embedded" json:"-"`

	// 流量统计
	TotalBytes  int64 `gorm:"default:0" json:"total_bytes"`
	InputBytes  int64 `gorm:"default:0" json:"input_bytes"`
	OutputBytes int64 `gorm:"default:0" json:"output_bytes"`

	TrafficLimitBytes       int64  `gorm:"not null;default:0" json:"traffic_limit_bytes"`
	TrafficResetPeriod      string `gorm:"size:10;not null;default:none" json:"traffic_reset_period"`
	TrafficQuotaUsedBytes   uint64 `gorm:"not null;default:0" json:"traffic_quota_used_bytes"`
	TrafficQuotaState       string `gorm:"size:16;not null;default:''" json:"traffic_quota_state"`
	TrafficQuotaWindowStart int64  `gorm:"not null;default:-1" json:"-"`

	LastCheckAt *time.Time     `json:"last_check_at"`           // 最后检查时间
	Remark      string         `gorm:"type:text" json:"remark"` // 备注
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`

	// 关联 - 规则
	Rules []GostRule `gorm:"foreignKey:NodeID" json:"rules,omitempty"`

	// 关联 - 隧道（作为入口或出口节点）
	EntryTunnels []GostTunnel `gorm:"foreignKey:EntryNodeID" json:"entry_tunnels,omitempty"`
	ExitTunnels  []GostTunnel `gorm:"foreignKey:ExitNodeID" json:"exit_tunnels,omitempty"`
}

// TableName 指定表名
func (GostNode) TableName() string {
	return "nodes"
}

// TrafficQuotaWindow returns the period start in Unix seconds, or zero for a lifetime quota.
func TrafficQuotaWindow(period string, now time.Time) int64 {
	now = now.In(trafficQuotaLocation)
	year, month, day := now.Date()
	switch period {
	case "daily":
	case "weekly":
		day -= (int(now.Weekday()) + 6) % 7
	case "monthly":
		day = 1
	default:
		return 0
	}
	return time.Date(year, month, day, 0, 0, 0, 0, trafficQuotaLocation).Unix()
}
