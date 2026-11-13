package service

import (
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/logger"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *TunnelService) applySharedFixedIngress(client *gost.Client, endpoint *model.GostRule, tunnels []*model.GostTunnel) error {
	targets := make([]string, 0, len(tunnels))
	for _, tunnel := range tunnels {
		address, err := ensureSharedFixedIngressRoute(client, endpoint, tunnel)
		if err != nil {
			logger.Warnf("配置入口 %d 的隧道 %d 本机转发失败: %v", endpoint.ID, tunnel.ID, err)
			return errors.ErrRuleStartFailed
		}
		targets = append(targets, address)
	}

	listener := buildFixedIngressService(endpoint, entranceServiceName(endpoint.ID), endpoint.ListenIP, endpoint.ListenPort, targets, "round")
	rules := NewRuleService(s.tunnelRepo.DB)
	if err := rules.setupRuleObserver(client, endpoint, listener); err != nil {
		return err
	}
	if err := client.ReplaceService(listener); err != nil {
		if code := status.Code(err); code == codes.InvalidArgument || code == codes.Unimplemented {
			return errors.ErrTunnelSharedDaemon
		}
		logger.Warnf("配置入口 %d 的共享监听失败: %v", endpoint.ID, err)
		return errors.ErrRuleStartFailed
	}
	endpoint.ServiceID = listener.Name
	return nil
}

func ensureSharedFixedIngressRoute(client *gost.Client, endpoint *model.GostRule, tunnel *model.GostTunnel) (string, error) {
	const loopback = "127.0.0.1"
	name := sharedFixedIngressServiceName(endpoint.ID, tunnel.ID)
	existing, err := client.Service(name)
	if err != nil {
		return "", err
	}
	if existing != nil {
		host, _, err := net.SplitHostPort(existing.Addr)
		if err != nil || host != loopback || existing.Handler == nil || existing.Handler.Chain != tunnel.ChainID ||
			existing.Listener == nil || existing.Listener.Type != string(endpoint.Protocol) {
			return "", fmt.Errorf("invalid local forwarding service %s", name)
		}
		return existing.Addr, nil
	}

	policy := endpointTargetRule(tunnel)
	for range 3 {
		port := 49152 + rand.IntN(16384)
		route := buildFixedIngressService(endpoint, name, loopback, port, policy.Targets, policy.Strategy)
		route.Handler.Chain = tunnel.ChainID
		if _, err = client.EnsureService(route); err == nil {
			return route.Addr, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "address already in use") {
			return "", err
		}
	}
	return "", err
}

func buildFixedIngressService(endpoint *model.GostRule, name, host string, port int, targets []string, strategy string) *gost.ServiceConfig {
	if len(targets) == 1 {
		strategy = "round"
	}
	var service *gost.ServiceConfig
	if endpoint.Protocol == model.RuleProtocolUDP {
		service = gost.BuildUDPForwardService(name, port, targets, strategy)
	} else {
		service = gost.BuildTCPForwardService(name, port, targets, strategy)
	}
	service.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	return service
}

func sharedFixedIngressServiceName(entryID, tunnelID uint) string {
	return fmt.Sprintf("rule-%d-tunnel-%d", entryID, tunnelID)
}
