package service

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"yagp/internal/model"
	"yagp/pkg/gost"
)

func (r *TunnelTestResult) inspectExitRelay(ctx context.Context, tunnel *model.GostTunnel, client *gost.Client) {
	configCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	config, err := client.GetConfigContext(configCtx)
	cancel()
	if err != nil {
		r.checkExitGostService(ctx, client)
		return
	}

	name := tunnel.ServiceID
	if name == "" {
		name = relayServiceName(tunnel.ID)
	}
	for _, relay := range config.Services {
		if relay.Name == name {
			r.checkExitRelay(tunnel, relay, config.Admissions)
			return
		}
	}
	r.fail("exit_relay", "出口 Gost 配置中没有此隧道的 Relay 服务。", "请在出口检查 Gost 服务，必要时重新启动隧道以创建 Relay。")
}

func (r *TunnelTestResult) checkExitGostService(ctx context.Context, client *gost.Client) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	service, err := client.GostServiceStatus(ctx)
	if err == nil {
		switch service.GetActiveState() {
		case "failed", "inactive":
			r.fail("exit_relay", "出口系统服务报告 Gost 已停止或运行失败。", "请在出口节点管理中检查 Gost 的服务状态和日志，恢复后重新测试。")
			return
		case "active":
			r.record("exit_relay", "warning", "出口 Gost 服务正在运行，但管理接口不可用，无法确认 Relay 状态。")
			return
		}
	}
	r.record("exit_relay", "warning", "未能读取出口 Gost 管理接口，无法确认 Relay 状态。")
}

func (r *TunnelTestResult) checkExitRelay(tunnel *model.GostTunnel, relay gost.ServiceConfig, admissions []gost.AdmissionConfig) {
	if relay.Status != nil && (relay.Status.State == "failed" || relay.Status.State == "closed") {
		r.fail("exit_relay", "出口 Gost 报告 Relay 已失败或关闭。", "请检查出口 Gost 日志、监听地址和端口占用。")
		return
	}
	_, port, addrErr := net.SplitHostPort(relay.Addr)
	if relay.Handler == nil || relay.Handler.Type != "relay" || relay.Listener == nil || relay.Listener.Type != tunnel.Protocol ||
		addrErr != nil || port != strconv.Itoa(tunnel.RelayPort) {
		r.fail("exit_relay", "出口 Gost 的处理器、传输协议或监听端口与面板保存的 Relay 配置不符。", "请核对目的端点配置，必要时重新启动隧道以同步 Relay。")
		return
	}
	if relay.Status != nil && (relay.Status.State == "ready" || relay.Status.State == "running") {
		r.record("exit_relay", "passed", fmt.Sprintf("出口 Gost 报告 Relay 就绪，传输协议 %s、监听端口 %d 与面板配置一致。", tunnel.Protocol, tunnel.RelayPort))
	} else {
		r.record("exit_relay", "warning", "出口 Relay 的配置存在，但 Gost 未提供可确认的运行状态。")
	}
	if r.relayTargetError == "relay_target_forbidden" && relay.Bypass != "" {
		// The Relay checks its target policy after processing authentication.
		r.Partial = true
		r.record("relay_handshake", "passed", "出口 Relay 已响应请求并通过认证检查。")
		r.fail("exit_target_policy", "出口目标策略拒绝了临时回环测试目标（127.0.0.1）。", tunnelProbeTargetRecommendation)
		r.record("relay_round_trip", "skipped", "临时测试目标未获放行，未执行测试数据往返。")
	}

	var whitelistEntries []string
	whitelistEnabled := false
	for _, admission := range admissions {
		if admission.Whitelist && relay.HasAdmission(admission.Name) {
			whitelistEnabled = true
			whitelistEntries = append(whitelistEntries, admission.Matchers...)
		}
	}
	if whitelistEnabled {
		state := "skipped"
		message := "出口 Relay 启用了来源白名单，本次探测未能确认来源准入结果。"
		// Gost closes rejected connections before processing the Relay request.
		if r.relayTargetError != "" {
			state = "passed"
			message = "本次探测已通过出口 Relay 的来源白名单检查。"
		}
		if len(whitelistEntries) == 0 {
			message += "当前配置未列出白名单条目。"
		} else {
			message += "当前配置的白名单条目：" + strings.Join(whitelistEntries, "、") + "。"
		}
		r.record("relay_policy", state, message)
	}
	if tunnel.ExitNode.TrafficQuotaState == model.TrafficQuotaBlocked {
		r.record("relay_quota", "warning", "面板显示出口节点的流量配额已阻断，请确认此 Relay 端口是否受限。")
	}
}
