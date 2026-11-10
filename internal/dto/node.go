// Package dto 定义数据传输对象
package dto

import "time"

// ==================== 节点相关 ====================

// CreateNodeReq 创建节点请求
type CreateNodeReq struct {
	Name               string  `json:"name" binding:"required,min=1,max=100"` // 节点名称
	Remark             string  `json:"remark"`                                // 备注
	TrafficLimitBytes  *int64  `json:"traffic_limit_bytes" binding:"omitempty,gte=0,lte=9007199254740991"`
	TrafficResetPeriod *string `json:"traffic_reset_period" binding:"omitempty,oneof=none daily weekly monthly"`
}

// UpdateNodeReq 更新节点请求
type UpdateNodeReq struct {
	Name               string   `json:"name" binding:"required,min=1,max=100"` // 节点名称
	Remark             string   `json:"remark"`                                // 备注
	TrafficLimitBytes  *int64   `json:"traffic_limit_bytes" binding:"omitempty,gte=0,lte=9007199254740991"`
	TrafficResetPeriod *string  `json:"traffic_reset_period" binding:"omitempty,oneof=none daily weekly monthly"`
	AdditionalIPs      []string `json:"additional_ips"`
}

type NodeRegistrationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NodeListReq 节点列表请求
type NodeListReq struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`             // 页码
	PageSize int    `form:"pageSize" binding:"omitempty,min=1,max=100"` // 每页数量
	Status   string `form:"status"`                                     // 状态筛选
	Keyword  string `form:"keyword"`                                    // 关键词搜索
}

// SetDefaults 设置默认值
func (r *NodeListReq) SetDefaults() {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = 10
	}
}
