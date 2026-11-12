package model

import (
	"time"

	"gorm.io/gorm"
)

// RuleStatus 规则状态
type RuleStatus string

const (
	RuleStatusRunning RuleStatus = "running" // 运行中
	RuleStatusStopped RuleStatus = "stopped" // 已停止
	RuleStatusError   RuleStatus = "error"   // 错误
)

// RuleProtocol 规则协议
type RuleProtocol string

const (
	RuleProtocolTCP RuleProtocol = "tcp" // TCP 协议
	RuleProtocolUDP RuleProtocol = "udp" // UDP 协议
)

// ListenerNetwork is the socket transport used by a Gost listener.
func (p RuleProtocol) ListenerNetwork() string {
	switch p {
	case RuleProtocolUDP, "kcp", "quic", "h3":
		return "udp"
	default:
		return "tcp"
	}
}

type EndpointRole string

const (
	EndpointRoleEntry EndpointRole = "entry"
	EndpointRoleExit  EndpointRole = "exit"
)

// RuleType 规则类型
type RuleType string

const (
	RuleTypeForward RuleType = "forward" // 端口转发（直连目标）
	RuleTypeTunnel  RuleType = "tunnel"  // 隧道转发（通过隧道链路）
)

// ObserverCounters records the last cumulative counters reported by a Gost service.
type ObserverCounters struct {
	InputBytes  int64 `gorm:"not null;default:0"`
	OutputBytes int64 `gorm:"not null;default:0"`
	Connections int64 `gorm:"not null;default:0"`
}

// GostRule stores an ingress listener or an exit policy. Legacy standalone rules retain an empty Role.
type GostRule struct {
	ID         uint         `gorm:"primaryKey" json:"id"`
	NodeID     *uint        `gorm:"index" json:"node_id"`                         // 入口节点 ID（端口转发时使用）
	Name       string       `gorm:"size:100;not null" json:"name"`                // 规则名称
	Type       RuleType     `gorm:"size:20;not null;default:forward" json:"type"` // 规则类型
	Role       EndpointRole `gorm:"size:10;not null;default:''" json:"role"`      // 空值保留旧端点的行为
	TunnelID   *uint        `gorm:"index" json:"tunnel_id"`                       // 隧道 ID（隧道转发时使用）
	Protocol   RuleProtocol `gorm:"size:10;not null;default:tcp" json:"protocol"` // 入口监听或出口 Relay 协议
	ListenIP   string       `gorm:"size:45;not null;default:''" json:"listen_ip"` // 监听 IP，空字符串表示所有接口
	ListenPort int          `gorm:"not null" json:"listen_port"`                  // 监听端口

	ProtocolOptions map[string]string `gorm:"type:text;serializer:json" json:"protocol_options"`

	Targets         []string   `gorm:"type:json;serializer:json" json:"targets"` // 多目标列表 (host:port)
	UseClashRuleset bool       `gorm:"not null;default:false" json:"use_clash_ruleset"`
	Strategy        string     `gorm:"size:20;default:round" json:"strategy"` // 负载均衡策略 (round, random, fifo)
	EnableTLS       bool       `gorm:"default:false" json:"enable_tls"`       // 是否启用 TLS
	Status          RuleStatus `gorm:"size:20;default:stopped" json:"status"` // 状态
	ServiceID       string     `gorm:"size:100" json:"service_id"`            // Gost 服务 ID

	// 流量监控配置
	ObserverID string `gorm:"size:100" json:"observer_id"` // 观察器 ID

	// 流量统计 (由观察器更新)
	InputBytes       int64            `gorm:"default:0" json:"input_bytes"`    // 入站总流量 (bytes)
	OutputBytes      int64            `gorm:"default:0" json:"output_bytes"`   // 出站总流量 (bytes)
	TotalBytes       int64            `gorm:"default:0" json:"total_bytes"`    // 总流量 (Input + Output)
	TotalRequests    int64            `gorm:"default:0" json:"total_requests"` // 总请求数
	ObserverCounters ObserverCounters `gorm:"embedded;embeddedPrefix:observer_" json:"-"`

	Remark    string         `gorm:"type:text" json:"remark"` // 备注
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// 关联 - 入口节点
	Node *GostNode `gorm:"foreignKey:NodeID" json:"node,omitempty"`
	// 关联 - 隧道
	Tunnel *GostTunnel `gorm:"foreignKey:TunnelID" json:"tunnel,omitempty"`
	// 隧道选用该入口后，由查询结果填充绑定信息。
	LinkedTunnelID     *uint        `gorm:"-" json:"linked_tunnel_id,omitempty"`
	TunnelRole         string       `gorm:"-" json:"tunnel_role,omitempty"`
	LinkedTunnelStatus TunnelStatus `gorm:"-" json:"linked_tunnel_status,omitempty"`
}

// TableName 指定表名
func (GostRule) TableName() string {
	return "rules"
}
