package service

import (
	"context"
	"fmt"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
)

func validateClashTarget(useClash bool, protocol model.RuleProtocol, listenIP string, targets []string) error {
	if useClash && (protocol != model.RuleProtocolTCP || listenIP != "127.0.0.1" || len(targets) != 0) {
		return errors.ErrRuleClashInvalid
	}
	return nil
}

func clashTunnelChainName(id uint) string {
	return fmt.Sprintf("tunnel-%d-clash-chain", id)
}

func (s *RuleService) prepareClashChain(client *gost.Client, tunnel *model.GostTunnel) (string, error) {
	ruleset, err := s.rulesetService.Get(context.Background())
	if err != nil {
		return "", err
	}
	if ruleset.URL == "" || len(ruleset.Matchers) == 0 {
		return "", errors.ErrRuleClashUnavailable
	}
	name := clashTunnelChainName(tunnel.ID)
	if _, err := client.SetTransparentBypass(ruleset.Matchers); err != nil {
		logger.Warnf("配置 Clash 分流白名单失败: %v", err)
		return "", errors.ErrRuleChainCreateFailed
	}
	if err := client.EnsureClashChain(tunnel.ChainID, name); err != nil {
		logger.Warnf("创建 Clash 分流链失败: %v", err)
		return "", errors.ErrRuleChainCreateFailed
	}
	return name, nil
}
