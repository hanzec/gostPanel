package dto

// ==================== 规则管理相关 ====================

// CreateRuleReq 创建节点入口。已有的隧道规则仍使用 type 和 tunnel_id。
type CreateRuleReq struct {
	NodeID     *uint  `json:"node_id"`                                        // 入口节点 ID
	TunnelID   *uint  `json:"tunnel_id"`                                      // 隧道 ID（已有隧道规则）
	Name       string `json:"name" binding:"required,min=1,max=100"`          // 规则名称
	Type       string `json:"type" binding:"omitempty,oneof=forward tunnel"`  // 规则类型，默认 forward
	Protocol   string `json:"protocol" binding:"required,oneof=tcp udp"`      // 协议类型
	ListenIP   string `json:"listen_ip"`                                      // 监听 IP，空字符串表示所有接口
	ListenPort int    `json:"listen_port" binding:"required,min=1,max=65535"` // 监听端口

	Targets         []string `json:"targets"` // 多目标列表
	UseClashRuleset bool     `json:"use_clash_ruleset"`
	Strategy        string   `json:"strategy" binding:"omitempty,oneof=round rand fifo hash"` // 负载均衡策略
	EnableTLS       bool     `json:"enable_tls"`                                              // 是否启用 TLS

	Remark string `json:"remark"` // 备注
}

// UpdateRuleReq 更新规则请求
type UpdateRuleReq struct {
	NodeID     *uint   `json:"node_id"`                                        // 省略时保留已有节点
	Name       string  `json:"name" binding:"required,min=1,max=100"`          // 规则名称
	Protocol   string  `json:"protocol" binding:"required,oneof=tcp udp"`      // 协议类型
	ListenIP   *string `json:"listen_ip"`                                      // 省略时保留已有监听 IP
	ListenPort int     `json:"listen_port" binding:"required,min=1,max=65535"` // 监听端口

	Targets         []string `json:"targets"` // 多目标列表
	UseClashRuleset *bool    `json:"use_clash_ruleset"`
	Strategy        string   `json:"strategy" binding:"omitempty,oneof=round rand fifo hash"` // 负载均衡策略
	EnableTLS       bool     `json:"enable_tls"`                                              // 是否启用 TLS

	Remark string `json:"remark"` // 备注
}

// RuleListReq 规则列表请求
type RuleListReq struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`             // 页码
	PageSize int    `form:"pageSize" binding:"omitempty,min=1,max=100"` // 每页数量
	NodeID   uint   `form:"node_id"`                                    // 节点 ID 筛选
	TunnelID uint   `form:"tunnel_id"`                                  // 隧道 ID 筛选
	Type     string `form:"type"`                                       // 规则类型筛选
	Status   string `form:"status"`                                     // 状态筛选
	Keyword  string `form:"keyword"`                                    // 关键词搜索
}

// SetDefaults 设置默认值
func (r *RuleListReq) SetDefaults() {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = 10
	}
}
