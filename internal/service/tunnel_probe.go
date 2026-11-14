package service

import (
	"context"
	"time"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	tunnelTestTimeout = 11 * time.Second
	tunnelProbeEntry  = "入口节点"
	tunnelProbeExit   = "出口节点"
)

type TunnelTestResult struct {
	Success        bool                   `json:"success"`
	Partial        bool                   `json:"partial,omitempty"`
	LatencyMS      int64                  `json:"latency_ms"`
	FailureStage   string                 `json:"failure_stage,omitempty"`
	Diagnostics    []TunnelTestDiagnostic `json:"diagnostics"`
	Recommendation string                 `json:"recommendation,omitempty"`

	relayTargetError string
}

type TunnelTestDiagnostic struct {
	Stage   string `json:"stage"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (r *TunnelTestResult) record(stage, state, message string) {
	check := TunnelTestDiagnostic{Stage: stage, Status: state, Message: message}
	for i, existing := range r.Diagnostics {
		if existing.Stage == stage {
			r.Diagnostics[i] = check
			return
		}
	}
	r.Diagnostics = append(r.Diagnostics, check)
}

func (r *TunnelTestResult) fail(stage, message, recommendation string) {
	r.record(stage, "failed", message)
	r.FailureStage = stage
	r.Recommendation = recommendation
}

// Test probes the saved relay chain using a temporary listener on the exit node.
// A relay with a destination policy may reject the temporary target after authenticating.
func (s *TunnelService) Test(ctx context.Context, id uint) (*TunnelTestResult, error) {
	tunnel, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	result := &TunnelTestResult{}
	if err := result.checkTunnel(tunnel); err != nil {
		return result, err
	}

	exitClient := utils.GetGostClient(tunnel.ExitNode)
	probeCtx, cancel := context.WithTimeout(ctx, tunnelTestTimeout)
	defer cancel()
	endpoint, err := exitClient.StartTunnelProbe(probeCtx)
	if err != nil {
		businessErr := tunnelProbeError(err, tunnelProbeExit)
		logger.Warnf("隧道 %d 出口测试准备失败（%s）", id, status.Code(err))
		if message := tunnelProbeSessionFailure(err); message != "" {
			result.fail("exit_node", message, "请确认出口 yagp-daemon 已注册并连接面板。")
			return result, businessErr
		}
		result.recordExitListenerFailure(status.Code(err), businessErr)
		return result, businessErr
	}
	result.record("exit_listener", "passed", "出口 daemon 已启动临时回环测试监听。")

	reply, err := utils.GetGostClient(tunnel.EntryNode).ProbeTunnel(probeCtx, &nodegrpc.TunnelProbeRequest{
		ChainName: tunnel.ChainID, Address: endpoint.GetAddress(), Token: endpoint.GetToken(),
	})
	if err != nil {
		cancel()
		businessErr := tunnelProbeError(err, tunnelProbeEntry)
		if result.recordEntryFailure(err, businessErr) {
			// The probe may have consumed its deadline. Give this read-only check its own budget.
			result.inspectExitRelay(ctx, tunnel, exitClient)
		}
		if result.Partial {
			return result, nil
		}
		logger.Warnf("隧道 %d 测试在 %s 阶段失败（%s）", id, result.FailureStage, status.Code(err))
		return result, businessErr
	}
	result.Success = true
	result.LatencyMS = reply.GetRoundTripMs()
	result.record("entry_chain", "passed", "入口 Gost 已加载隧道转发链。")
	result.record("entry_gost_process", "passed", "入口的临时 Gost 测试进程已就绪。")
	result.record("relay_round_trip", "passed", "测试数据已通过 Relay 往返，返回内容校验通过。")
	return result, nil
}

func (r *TunnelTestResult) checkTunnel(t *model.GostTunnel) error {
	switch {
	case t.Status != model.TunnelStatusRunning:
		r.fail("tunnel", "面板记录的隧道状态为未运行。", "请启动隧道后重新测试。")
		return errors.ErrTunnelNotRunning
	case t.ChainID == "":
		r.fail("tunnel", "面板未记录此隧道的入口 Gost 转发链。", "请重新启动隧道以生成转发链。")
		return errors.ErrTunnelChainNotFound
	}
	r.record("tunnel", "passed", "隧道已启动，面板已保存入口转发链。")

	if t.EntryNode != nil && t.EntryNode.Status == model.NodeStatusOnline {
		r.record("entry_node", "passed", "面板记录的入口节点状态为在线。")
	} else {
		r.fail("entry_node", "入口节点离线或不可用。", "请确认入口 yagp-daemon 已连接面板。")
	}
	if t.ExitNode != nil && t.ExitNode.Status == model.NodeStatusOnline {
		r.record("exit_node", "passed", "面板记录的出口节点状态为在线。")
	} else if r.FailureStage == "" {
		r.fail("exit_node", "出口节点离线或不可用。", "请确认出口 yagp-daemon 已连接面板。")
	} else {
		r.record("exit_node", "failed", "出口节点也离线或不可用。")
	}
	return endpointNodesOnline(t)
}

func (r *TunnelTestResult) recordExitListenerFailure(code codes.Code, businessErr *errors.BizError) {
	switch code {
	case codes.Unimplemented:
		r.fail("exit_listener", "出口 daemon 版本不支持隧道测试。", businessErr.Message)
	case codes.DeadlineExceeded:
		r.fail("exit_listener", "等待出口 daemon 准备测试监听超时。", "请确认出口 daemon 与面板的连接稳定，再重新测试。")
	default:
		r.fail("exit_listener", "出口 daemon 未能提供临时测试监听。", "请检查出口 daemon 日志和本机回环地址是否可用。")
	}
	r.record("relay_round_trip", "skipped", "测试在准备阶段结束。")
}

func (r *TunnelTestResult) recordEntryFailure(err error, businessErr *errors.BizError) bool {
	code := status.Code(err)
	sessionFailure := tunnelProbeSessionFailure(err)
	var reason string
	var evidence map[string]string
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "yagp.tunnel_probe" {
			reason, evidence = info.GetReason(), info.GetMetadata()
			break
		}
	}
	switch {
	case sessionFailure != "":
		r.fail("entry_node", sessionFailure, "请确认入口 yagp-daemon 已注册并连接面板。")
	case code == codes.Unimplemented:
		r.fail("entry_node", "入口 daemon 版本不支持隧道测试。", businessErr.Message)
	case reason == "entry_gost_api":
		r.fail("entry_gost_api", "无法读取入口 Gost 管理接口中的隧道配置。", "请检查入口 Gost 服务和本机管理接口的配置。")
		if evidence["gost_error"] == "admin_authentication" {
			r.Recommendation = "入口 Gost 管理接口拒绝认证，请核对 Gost 管理账号和 daemon 配置。"
		}
	case reason == "entry_chain" || code == codes.NotFound || code == codes.FailedPrecondition:
		message := "入口 Gost 中的隧道转发链不符合测试要求。"
		if code == codes.NotFound {
			message = "入口 Gost 中找不到此隧道的转发链。"
		}
		r.fail("entry_chain", message, "请检查入口 Gost 的转发链配置，必要时重新启动隧道。")
	case reason == "entry_gost_process":
		r.record("entry_chain", "passed", "已读取入口 Gost 的隧道转发链。")
		r.fail("entry_gost_process", "入口节点未能启动或连接临时 Gost 测试进程。", "请检查入口 Gost 的安装、版本和运行权限。")
		r.recordGostEvidence(evidence["gost_error"])
	case code == codes.Internal:
		r.fail("entry_gost_process", "入口 daemon 无法准备 Gost 测试。", businessErr.Message)
	default:
		r.recordRoundTripFailure(code, reason, evidence)
		return true
	}
	r.record("relay_round_trip", "skipped", "测试在准备阶段结束。")
	return false
}

func tunnelProbeSessionFailure(err error) string {
	switch status.Convert(err).Message() {
	case "节点尚未通过令牌注册":
		return "此节点尚未通过令牌注册。"
	case "节点尚未连接面板", "node stream disconnected":
		return "面板与此节点的 daemon 没有可用连接。"
	default:
		return ""
	}
}

func (r *TunnelTestResult) recordRoundTripFailure(code codes.Code, reason string, evidence map[string]string) {
	message := "入口 daemon 未能确认测试数据完成往返。"
	switch reason {
	case "relay_write", "relay_read", "relay_response":
		r.record("entry_chain", "passed", "已读取入口 Gost 的隧道转发链。")
		r.record("entry_gost_process", "passed", "入口的临时 Gost 测试进程已就绪。")
	}
	switch {
	case reason == "relay_write":
		message = "测试数据未能写入入口的临时 Gost 连接。"
	case reason == "relay_response" || code == codes.DataLoss:
		message = "收到了返回数据，但内容校验失败。"
	case reason == "relay_read" && code == codes.DeadlineExceeded:
		message = "等待隧道返回完整测试数据超时。"
	case reason == "relay_read":
		message = "连接关闭前未收到完整的测试响应。"
	case code == codes.DeadlineExceeded:
		message = "入口测试未能在时限内完成。"
	}
	r.fail("relay_round_trip", message, "请核对入口与出口的 Relay 协议、认证和出口来源白名单，再检查节点间的网络。")
	r.recordRelayTCP(evidence["relay_tcp"])
	r.recordGostEvidence(evidence["gost_error"])
}

func (r *TunnelTestResult) recordRelayTCP(outcome string) {
	switch outcome {
	case "reachable":
		r.record("relay_connection", "passed", "入口可以建立到转发链配置地址的 TCP 连接。")
	case "refused":
		r.fail("relay_connection", "转发链配置的 TCP 地址拒绝了入口节点的连接。", "请核对出口 Relay 的监听地址和端口，以及防火墙或云安全组是否主动拒绝连接。")
	case "timeout":
		r.fail("relay_connection", "入口未能在短时检查内连上转发链配置的 TCP 地址。", "请检查出口安全组、防火墙、端口映射以及节点之间的路由。")
	case "unreachable":
		r.fail("relay_connection", "入口无法路由到转发链配置的地址。", "请检查出口连接地址、IP 版本和入口节点路由。")
	case "dns_error":
		r.fail("relay_connection", "入口无法解析转发链配置的 Relay 主机名。", "请检查入口 DNS 和 Relay 主机名。")
	case "unavailable":
		r.record("relay_connection", "warning", "未能建立 TCP 连接，具体原因未确认。")
	case "not_applicable":
		r.record("relay_connection", "skipped", "此 Relay 使用基于 UDP 的传输，TCP 连通性检查不适用。")
	}
}

func (r *TunnelTestResult) recordGostEvidence(outcome string) {
	switch outcome {
	case "binary_missing":
		r.Recommendation = "入口节点安装目录中找不到 Gost 可执行文件，请检查 Gost 安装。"
	case "binary_permission":
		r.Recommendation = "入口 daemon 没有执行 Gost 的权限，请检查文件权限和挂载选项。"
	case "dialer_unsupported":
		r.Recommendation = "入口 Gost 不支持转发链配置的传输协议，请检查版本或重新配置协议。"
	case "relay_authentication":
		r.fail("relay_handshake", "Gost Relay 拒绝了认证。", "请核对入口转发链与出口 Relay 的认证配置。")
	case "relay_target_unreachable":
		r.relayTargetError = outcome
		r.fail("relay_handshake", "Gost Relay 报告无法连接临时测试目标。", "请检查出口 Gost 能否访问 daemon 所在网络命名空间的回环地址，以及出口是否限制了回环访问。")
	case "relay_target_forbidden":
		r.relayTargetError = outcome
		r.fail("relay_handshake", "出口 Relay 的目标策略拒绝了临时回环测试目标（127.0.0.1）。", "请通过入口使用出口目标策略允许的业务地址验证转发。")
	case "relay_handshake":
		r.fail("relay_handshake", "Gost 报告传输握手失败。", "请核对两端传输协议及其参数，例如 WebSocket 路径。")
	case "relay_tls":
		r.fail("relay_handshake", "Gost 报告 TLS 握手失败。", "请核对 TLS 证书、SNI、校验设置和两端支持的 TLS 版本。")
	}
}

func tunnelProbeError(err error, nodeRole string) *errors.BizError {
	code := status.Code(err)
	switch code {
	case codes.Unimplemented:
		return errors.TunnelProbeDaemonUnsupported(nodeRole)
	case codes.DeadlineExceeded:
		return errors.ErrTunnelProbeTimeout
	}
	if nodeRole == tunnelProbeExit {
		return errors.ErrTunnelProbePrepare
	}
	switch code {
	case codes.NotFound:
		return errors.ErrTunnelChainNotFound
	case codes.FailedPrecondition:
		return errors.ErrTunnelProbeChain
	case codes.Internal:
		return errors.ErrTunnelProbeGost
	case codes.DataLoss:
		return errors.ErrTunnelProbeData
	default:
		return errors.ErrTunnelProbeFailed
	}
}
