// Package dto 定义数据传输对象
package dto

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"yagp/internal/model"
)

// ==================== 节点相关 ====================

// CreateNodeReq 创建节点请求
type CreateNodeReq struct {
	Name               string              `json:"name" binding:"required,min=1,max=100"` // 节点名称
	AddressMappings    NodeAddressMappings `json:"address_mappings"`
	Remark             string              `json:"remark"` // 备注
	TrafficLimitBytes  *int64              `json:"traffic_limit_bytes" binding:"omitempty,gte=0,lte=9007199254740991"`
	TrafficResetPeriod *string             `json:"traffic_reset_period" binding:"omitempty,oneof=none daily weekly monthly"`
}

// UpdateNodeReq 更新节点请求
type UpdateNodeReq struct {
	Name               string              `json:"name" binding:"required,min=1,max=100"` // 节点名称
	AddressMappings    NodeAddressMappings `json:"address_mappings"`
	Remark             string              `json:"remark"` // 备注
	TrafficLimitBytes  *int64              `json:"traffic_limit_bytes" binding:"omitempty,gte=0,lte=9007199254740991"`
	TrafficResetPeriod *string             `json:"traffic_reset_period" binding:"omitempty,oneof=none daily weekly monthly"`
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

// NodeAddressMappings normalizes IP pairs at the JSON boundary. Nil preserves existing pairs.
type NodeAddressMappings []model.NodeAddressMapping

func (m *NodeAddressMappings) UnmarshalJSON(data []byte) error {
	var mappings []model.NodeAddressMapping
	if err := json.Unmarshal(data, &mappings); err != nil {
		return fmt.Errorf("监听 IP 映射必须为本机监听 IP 和对外连接 IP 的数组: %w", err)
	}
	seen := make(map[string]bool, len(mappings))
	for i := range mappings {
		mapping := &mappings[i]
		listen, err := parseMappingIP(mapping.ListenIP)
		if err != nil {
			return fmt.Errorf("第 %d 组映射的本机监听 IP: %w", i+1, err)
		}
		connect, err := parseMappingIP(mapping.ConnectIP)
		if err != nil {
			return fmt.Errorf("第 %d 组映射的对外连接 IP: %w", i+1, err)
		}
		if seen[listen] {
			return fmt.Errorf("本机监听 IP %q 重复映射", listen)
		}
		seen[listen] = true
		*mapping = model.NodeAddressMapping{ListenIP: listen, ConnectIP: connect}
	}
	*m = mappings
	return nil
}

func parseMappingIP(value string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.Zone() != "" || !ip.Unmap().IsGlobalUnicast() {
		return "", fmt.Errorf("%q 必须是有效的单播 IPv4 或 IPv6 地址，不能包含端口、网段或接口区域", value)
	}
	return ip.Unmap().String(), nil
}
