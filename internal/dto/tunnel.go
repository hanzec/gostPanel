package dto

// ==================== 隧道管理相关 ====================

// CreateTunnelReq 创建隧道请求
type CreateTunnelReq struct {
	Name             string   `json:"name" binding:"required,min=1,max=100"` // 隧道名称
	EntryRuleID      uint     `json:"entry_rule_id" binding:"required_without=EntryNodeID"`
	ExitRuleID       uint     `json:"exit_rule_id" binding:"required_without=ExitNodeID"`
	EntryNodeID      uint     `json:"entry_node_id" binding:"required_without=EntryRuleID"`
	ExitNodeID       uint     `json:"exit_node_id" binding:"required_without=ExitRuleID"`
	Protocol         string   `json:"protocol" binding:"required_without=EntryRuleID,omitempty,oneof=tcp mtcp udp tls mtls ws mws wss mwss h2 h2c grpc pht phts kcp quic h3 ohttp otls"`
	RelayPort        int      `json:"relay_port" binding:"required_without=ExitRuleID,omitempty,min=1,max=65535"`
	Remark           string   `json:"remark"` // 备注
	AllowlistNodeIDs []uint   `json:"allowlist_node_ids" binding:"omitempty,dive,min=1"`
	Targets          []string `json:"targets"`
	UseClashRuleset  bool     `json:"use_clash_ruleset"`
	Strategy         string   `json:"strategy" binding:"omitempty,oneof=round rand fifo hash"`

	ProtocolOptions map[string]string `json:"protocol_options"`
}

// UpdateTunnelReq 更新隧道请求
type UpdateTunnelReq struct {
	Name             string   `json:"name" binding:"required,min=1,max=100"` // 隧道名称
	EntryRuleID      uint     `json:"entry_rule_id" binding:"required_with=ExitRuleID"`
	ExitRuleID       uint     `json:"exit_rule_id" binding:"required_with=EntryRuleID"`
	Protocol         string   `json:"protocol" binding:"required_without=EntryRuleID,omitempty,oneof=tcp mtcp udp tls mtls ws mws wss mwss h2 h2c grpc pht phts kcp quic h3 ohttp otls"`
	RelayPort        int      `json:"relay_port" binding:"required_without=ExitRuleID,omitempty,min=1,max=65535"`
	Remark           string   `json:"remark"` // 备注
	AllowlistNodeIDs []uint   `json:"allowlist_node_ids" binding:"omitempty,dive,min=1"`
	Targets          []string `json:"targets"`
	UseClashRuleset  bool     `json:"use_clash_ruleset"`
	Strategy         string   `json:"strategy" binding:"omitempty,oneof=round rand fifo hash"`

	ProtocolOptions map[string]string `json:"protocol_options"`
}

type UpdateTunnelAllowlistReq struct {
	AllowlistNodeIDs []uint `json:"allowlist_node_ids" binding:"required,dive,min=1"`
}

// TunnelListReq 隧道列表请求
type TunnelListReq struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`             // 页码
	PageSize int    `form:"pageSize" binding:"omitempty,min=1,max=100"` // 每页数量
	NodeID   uint   `form:"node_id"`                                    // 节点 ID 筛选
	Status   string `form:"status"`                                     // 状态筛选
	Keyword  string `form:"keyword"`                                    // 关键词搜索
}

// SetDefaults 设置默认值
func (r *TunnelListReq) SetDefaults() {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = 10
	}
}
