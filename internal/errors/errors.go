// Package errors 定义统一的业务错误
// 所有业务错误都应该在此包中定义，便于统一管理和维护
package errors

import "net/http"

// BizError 业务错误结构
// 用于封装业务逻辑中的错误，包含错误码、错误消息和 HTTP 状态码
type BizError struct {
	Code     int    `json:"code"`    // 业务错误码
	Message  string `json:"message"` // 错误消息
	HTTPCode int    `json:"-"`       // HTTP 状态码（不序列化）
}

// Error 实现 error 接口
func (e *BizError) Error() string {
	return e.Message
}

// New 创建业务错误
func New(code int, message string, httpCode int) *BizError {
	return &BizError{
		Code:     code,
		Message:  message,
		HTTPCode: httpCode,
	}
}

// InvalidRulesetSelection preserves field-specific validation details for API callers.
func InvalidRulesetSelection(message string) *BizError {
	return New(10421, message, http.StatusBadRequest)
}

// ==================== 节点相关错误 (100xx) ====================

var (
	// ErrNodeNotFound 节点不存在
	ErrNodeNotFound = New(10001, "节点不存在", http.StatusNotFound)
	// ErrNodeNameExists 节点名称已存在
	ErrNodeNameExists = New(10002, "节点名称已存在", http.StatusBadRequest)
	// ErrNodeHasRules 节点下存在规则
	ErrNodeHasRules = New(10003, "节点下存在规则，无法删除", http.StatusBadRequest)
	// ErrNodeHasTunnels 节点下存在隧道配置
	ErrNodeHasTunnels = New(10004, "节点下存在隧道配置，无法删除", http.StatusBadRequest)
	// ErrNodeHasObservers 节点下存在流量监控
	ErrNodeHasObservers = New(10005, "节点下存在流量监控，无法删除", http.StatusBadRequest)
	// ErrNodeOffline 节点已离线
	ErrNodeOffline                 = New(10006, "节点已离线", http.StatusBadRequest)
	ErrNodeTrafficQuotaUnavailable = New(10007, "节点流量配额未能生效，请检查 yagp-daemon 和 nftables", http.StatusServiceUnavailable)
	ErrNodeOnline                  = New(10008, "节点已在线，无法重新安装", http.StatusConflict)
	ErrNodeMappingListenIP         = New(10009, "本机监听 IP 必须从节点已上报的 IP 中选择", http.StatusBadRequest)
)

// ==================== 规则相关错误 (101xx) ====================

var (
	// ErrRuleNotFound 规则不存在
	ErrRuleNotFound = New(10101, "规则不存在", http.StatusNotFound)
	// ErrRulePortExists 端口已被使用
	ErrRulePortExists = New(10102, "端口已被使用", http.StatusBadRequest)
	// ErrRuleRunning 规则正在运行中
	ErrRuleRunning = New(10103, "规则正在运行中，请先停止", http.StatusBadRequest)
	// ErrRuleStartFailed 启动规则失败
	ErrRuleStartFailed = New(10104, "启动规则失败", http.StatusInternalServerError)
	// ErrTunnelRequired 隧道转发需要选择隧道
	ErrTunnelRequired = New(10105, "隧道转发类型需要选择隧道", http.StatusBadRequest)
	// ErrRuleChainCreateFailed 创建规则链失败
	ErrRuleChainCreateFailed = New(10106, "创建规则链失败", http.StatusInternalServerError)
	// ErrNodeRequired 端口转发需要选择节点
	ErrNodeRequired = New(10107, "端口转发类型需要选择节点", http.StatusBadRequest)
	// ErrRuleTypeInvalid 无效的规则类型
	ErrRuleTypeInvalid = New(10108, "无效的规则类型", http.StatusBadRequest)
	// ErrTunnelChainNotFound 隧道链不存在
	ErrTunnelChainNotFound           = New(10109, "隧道未启动或链路不存在", http.StatusBadRequest)
	ErrRuleListenIPInvalid           = New(10110, "监听 IP 必须是 IPv4 或 IPv6 地址", http.StatusBadRequest)
	ErrRuleClashInvalid              = New(10111, "Clash 透明分流需要透明代理入口或旧版 TCP 和 127.0.0.1 监听，且不能填写固定目标", http.StatusBadRequest)
	ErrRuleClashUnavailable          = New(10112, "请先在系统设置中配置可用的 Clash 规则集", http.StatusBadRequest)
	ErrRuleTunnelLinked              = New(10113, "端点已绑定隧道，请在隧道管理中操作", http.StatusBadRequest)
	ErrRuleTargetRequired            = New(10114, "请在出口端点配置目标，再到隧道管理启动", http.StatusBadRequest)
	ErrEndpointProtocol              = New(10115, "入口支持 TCP、UDP 或透明代理，出口须选择 Gost Relay 通道", http.StatusBadRequest)
	ErrEndpointTargets               = New(10116, "入口无需目标；出口须使用原始目标、旧版 Clash 或固定目标，固定目标不能同时使用规则过滤", http.StatusBadRequest)
	ErrEndpointRole                  = New(10117, "请选择有效的入口或出口端点角色", http.StatusBadRequest)
	ErrRuleOriginalTargetInvalid     = New(10118, "原始目标转发需要透明代理入口，且不能填写固定目标", http.StatusBadRequest)
	ErrRuleTransparentListenIP       = New(10119, "透明代理入口的监听地址必须是回环地址", http.StatusBadRequest)
	ErrRuleTransparentTargetRequired = New(10120, "透明代理入口需要出口使用原始目标或旧版 Clash 规则集", http.StatusBadRequest)
)

// ==================== 隧道相关错误 (102xx) ====================

var (
	// ErrTunnelNotFound 隧道不存在
	ErrTunnelNotFound = New(10201, "隧道不存在", http.StatusNotFound)
	// ErrTunnelRunning 隧道正在运行中
	ErrTunnelRunning = New(10202, "隧道正在运行中，请先停止", http.StatusBadRequest)
	// ErrTunnelNameExists 隧道名称已存在
	ErrTunnelNameExists = New(10203, "隧道名称已存在", http.StatusBadRequest)
	// ErrTunnelInUse 隧道正在被规则使用
	ErrTunnelInUse = New(10209, "隧道正在被规则使用，无法删除", http.StatusBadRequest)
	// ErrTunnelRelayCreateFailed 创建隧道 Relay 服务失败
	ErrTunnelRelayCreateFailed = New(10214, "创建隧道 Relay 服务失败", http.StatusInternalServerError)
	// ErrTunnelHasRules 隧道被规则引用
	ErrTunnelHasRules = New(10215, "隧道被规则引用，无法删除", http.StatusBadRequest)
	// ErrEntryNodeOffline 入口节点离线
	ErrEntryNodeOffline = New(10216, "入口节点已离线", http.StatusBadRequest)
	// ErrExitNodeOffline 出口节点离线
	ErrExitNodeOffline = New(10217, "出口节点已离线", http.StatusBadRequest)
	// ErrTunnelChainCreateFailed 创建隧道 Chain 失败
	ErrTunnelChainCreateFailed = New(10218, "创建隧道Chain失败", http.StatusInternalServerError)
	// ErrTunnelObserverCreateFailed 创建观察器失败
	ErrTunnelObserverCreateFailed = New(10213, "创建观察器失败", http.StatusInternalServerError)
	ErrTunnelEndpointsRequired    = New(10220, "请选择两个端点来建立隧道", http.StatusBadRequest)
	ErrTunnelEndpointInUse        = New(10221, "端点已启用或已关联其他隧道，请选择空闲端点", http.StatusConflict)
	ErrTunnelEntryInvalid         = New(10222, "请选择有效的源端点", http.StatusBadRequest)
	ErrTunnelExitInvalid          = New(10223, "请选择监听地址可达的目的端点", http.StatusBadRequest)
	ErrTunnelEndpointSave         = New(10224, "保存节点 Gost 配置失败，请检查节点连接后重试", http.StatusBadGateway)
	ErrTunnelTransparentRedirect  = New(10225, "配置入口节点透明分流失败，请检查节点 nftables、守护程序版本和面板日志", http.StatusBadGateway)
	ErrTunnelTargetRequired       = New(10226, "请为隧道配置固定目标或选择 Clash 分流", http.StatusBadRequest)
	ErrTunnelTransparentDaemon    = New(10227, "入口节点的 yagp-daemon 不支持透明分流。请在节点管理中升级入口节点的 yagp-daemon，重启并确认升级后的进程已重新连接面板，再重试创建或启动隧道", http.StatusNotImplemented)
	ErrTunnelTransparentPlatform  = New(10228, "入口节点的透明分流需要 Linux；请确认 yagp-daemon 在 Linux 上使用节点配置文件启动", http.StatusNotImplemented)
	ErrTunnelTransparentNft       = New(10229, "入口节点未找到 nftables；请在入口节点安装 nftables，并确认 yagp-daemon 可以执行 nft 后重试", http.StatusNotImplemented)
	ErrTunnelProbePrepare         = New(10230, "无法在出口节点启动测试，请检查节点的 daemon 连接和运行状态", http.StatusBadGateway)
	ErrTunnelProbeFailed          = New(10231, "测试数据未能通过隧道往返，请检查入口节点和出口 Relay 的运行状态", http.StatusBadGateway)
	ErrTunnelProbeTimeout         = New(10232, "隧道转发测试超时，请检查两节点之间的连接和出口 Relay 的运行状态", http.StatusGatewayTimeout)
	ErrTunnelProbeChain           = New(10233, "入口节点无法使用 Gost 隧道链，请检查节点的连接和隧道配置", http.StatusConflict)
	ErrTunnelProbeGost            = New(10235, "入口节点无法准备 Gost 测试，请检查 Gost 安装和服务状态", http.StatusBadGateway)
	ErrTunnelProbeData            = New(10236, "隧道返回的测试数据校验失败，请检查入口转发链和出口 Relay", http.StatusBadGateway)
)

func TunnelProbeDaemonUnsupported(nodeRole string) *BizError {
	return New(10234, "请升级"+nodeRole+"的 yagp-daemon 后再测试隧道", http.StatusNotImplemented)
}

// ==================== 认证相关错误 (103xx) ====================

var (
	// ErrInvalidCredentials 用户名或密码错误
	ErrInvalidCredentials = New(10301, "用户名或密码错误", http.StatusUnauthorized)
	// ErrTokenExpired 登录已过期
	ErrTokenExpired = New(10302, "登录已过期，请重新登录", http.StatusUnauthorized)
	// ErrTokenInvalid Token 无效
	ErrTokenInvalid = New(10303, "Token 无效", http.StatusUnauthorized)
	// ErrPasswordMismatch 原密码错误
	ErrPasswordMismatch = New(10304, "原密码错误", http.StatusBadRequest)
	// ErrUserNotFound 用户不存在
	ErrUserNotFound = New(10305, "用户不存在", http.StatusNotFound)
)

// ==================== 通用错误 (500xx) ====================

var (
	// ErrInternal 服务器内部错误
	ErrInternal = New(50000, "服务器内部错误", http.StatusInternalServerError)
	// ErrBadRequest 请求参数错误
	ErrBadRequest = New(40000, "请求参数错误", http.StatusBadRequest)
	// ErrOperationFailed 操作失败
	ErrOperationFailed = New(50001, "操作失败", http.StatusInternalServerError)
)

// ==================== 认证相关错误 (103xx) ====================

var (
	// ErrTokenGenerationFailed Token 生成失败
	ErrTokenGenerationFailed = New(10306, "Token 生成失败", http.StatusInternalServerError)
)

// ==================== 系统/配置相关错误 (104xx) ====================

var (
	ErrReportNotConfigured = New(10401, "请在配置文件中启用 gRPC 流量上报后再启动规则", http.StatusBadRequest)
	// ErrSMTPConfigIncomplete SMTP配置不完整
	ErrSMTPConfigIncomplete = New(10402, "SMTP配置不完整", http.StatusBadRequest)
	// ErrSMTPConnectFailed 连接SMTP服务器失败
	ErrSMTPConnectFailed = New(10403, "连接SMTP服务器失败", http.StatusInternalServerError)
	// ErrSMTPClientFailed 创建SMTP客户端失败
	ErrSMTPClientFailed = New(10404, "创建SMTP客户端失败", http.StatusInternalServerError)
	// ErrSMTPAuthFailed SMTP认证失败
	ErrSMTPAuthFailed = New(10405, "SMTP认证失败", http.StatusUnauthorized)
	// ErrSMTPSenderFailed 设置发件人失败
	ErrSMTPSenderFailed = New(10406, "设置发件人失败", http.StatusInternalServerError)
	// ErrSMTPRecipientFailed 设置收件人失败
	ErrSMTPRecipientFailed = New(10407, "设置收件人失败", http.StatusInternalServerError)
	// ErrSMTPDataFailed 创建邮件数据流失败
	ErrSMTPDataFailed = New(10408, "创建邮件数据流失败", http.StatusInternalServerError)
	// ErrSMTPWriteFailed 写入邮件内容失败
	ErrSMTPWriteFailed = New(10409, "写入邮件内容失败", http.StatusInternalServerError)
	// ErrSMTPCloseFailed 关闭邮件数据流失败
	ErrSMTPCloseFailed = New(10410, "关闭邮件数据流失败", http.StatusInternalServerError)

	// ErrDBPathNotConfigured 未配置数据库路径
	ErrDBPathNotConfigured = New(10411, "未配置数据库路径", http.StatusInternalServerError)
	// ErrBackupDirCreateFailed 创建备份目录失败
	ErrBackupDirCreateFailed = New(10412, "创建备份目录失败", http.StatusInternalServerError)
	// ErrBackupFailed 备份失败
	ErrBackupFailed      = New(10413, "备份失败", http.StatusInternalServerError)
	ErrBackupUnsupported = New(10416, "面板仅支持 SQLite 备份。MySQL 请使用数据库服务备份。", http.StatusNotImplemented)

	// ErrObserverCreateFailed 创建观察器失败
	ErrObserverCreateFailed = New(10414, "创建流量监控失败", http.StatusInternalServerError)
	// ErrExtractHostFailed 提取主机IP失败
	ErrExtractHostFailed = New(10415, "无法从API地址提取主机IP", http.StatusInternalServerError)

	ErrBarkConfigIncomplete = New(10417, "请同时填写 Bark 服务器地址和设备密钥", http.StatusBadRequest)
	ErrBarkServerURLInvalid = New(10418, "请输入有效的 Bark 服务器地址，使用 HTTP 或 HTTPS 且不含账号、查询参数或片段", http.StatusBadRequest)
	ErrBarkDeviceKeyInvalid = New(10419, "Bark 设备密钥过长", http.StatusBadRequest)
	ErrBarkPushFailed       = New(10420, "Bark 测试推送失败，请检查服务器地址、设备密钥和面板网络连接", http.StatusBadGateway)
)

// ==================== 隧道相关补全 (102xx) ====================

var (
	// ErrTunnelNodeSame 入口和出口节点不能相同
	ErrTunnelNodeSame = New(10204, "入口和出口节点不能相同", http.StatusBadRequest)
	// ErrEntryNodeNotFound 入口节点不存在
	ErrEntryNodeNotFound = New(10205, "入口节点不存在", http.StatusNotFound)
	// ErrExitNodeNotFound 出口节点不存在
	ErrExitNodeNotFound = New(10206, "出口节点不存在", http.StatusNotFound)
	// ErrTunnelNotRunning 隧道未运行
	ErrTunnelNotRunning = New(10207, "隧道未运行", http.StatusBadRequest)
)
