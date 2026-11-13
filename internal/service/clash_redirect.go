package service

import (
	"context"
	"fmt"
	"strings"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
)

func validateTransparentTarget(useOriginal, useClash bool, protocol model.RuleProtocol, listenIP string, targets []string) error {
	if useOriginal && useClash {
		return errors.ErrEndpointTargets
	}
	if (useOriginal && protocol != model.RuleProtocolTransparent) ||
		((useOriginal || protocol == model.RuleProtocolTransparent) && len(targets) != 0) {
		return errors.ErrRuleOriginalTargetInvalid
	}
	if useClash && ((protocol != model.RuleProtocolTCP && protocol != model.RuleProtocolTransparent) || listenIP != "127.0.0.1" || len(targets) != 0) {
		return errors.ErrRuleClashInvalid
	}
	return nil
}

func clashTunnelChainName(id uint) string {
	return fmt.Sprintf("tunnel-%d-clash-chain", id)
}

func transparentPolicyName(id uint) string {
	return fmt.Sprintf("tunnel-%d-bypass", id)
}

func relayPolicyName(serviceID string) string {
	return strings.TrimPrefix(serviceID, "relay-") + "-bypass"
}

func hasSelectedRules(rule *model.GostRule) bool {
	return len(rule.RulesetIDs) != 0 || len(rule.ManualDomains) != 0
}

func transparentFiltering(rule *model.GostRule) bool {
	return rule.UseClashRuleset || hasSelectedRules(rule)
}

func (s *RuleService) prepareTransparentChain(client *gost.Client, tunnel *model.GostTunnel, rule *model.GostRule, mark uint32) (string, error) {
	name := clashTunnelChainName(tunnel.ID)
	bypass, err := s.prepareTransparentBypass(client, transparentPolicyName(tunnel.ID), rule)
	if err != nil {
		return "", err
	}
	if err := client.EnsureMarkedTransparentChain(tunnel.ChainID, name, mark, bypass); err != nil {
		logger.Warnf("创建 Clash 分流链失败: %v", err)
		return "", errors.ErrRuleChainCreateFailed
	}
	return name, nil
}

func (s *RuleService) prepareTransparentBypass(client *gost.Client, name string, rule *model.GostRule) (string, error) {
	bypass := ""
	if hasSelectedRules(rule) {
		matchers, err := s.rulesetService.SelectedMatchers(context.Background(), rule.RulesetIDs, rule.ManualDomains)
		if err != nil {
			return "", err
		}
		if len(matchers) == 0 {
			return "", errors.ErrRuleClashUnavailable
		}
		bypass = name
		if _, err := client.SetNamedTransparentBypass(bypass, matchers); err != nil {
			return "", errors.ErrRuleChainCreateFailed
		}
	} else if rule.UseClashRuleset {
		ruleset, err := s.rulesetService.Get(context.Background())
		if err != nil {
			return "", err
		}
		if ruleset.URL == "" || len(ruleset.Matchers) == 0 {
			return "", errors.ErrRuleClashUnavailable
		}
		if _, err := client.SetTransparentBypass(ruleset.Matchers); err != nil {
			logger.Warnf("配置 Clash 分流白名单失败: %v", err)
			return "", errors.ErrRuleChainCreateFailed
		}
		bypass = gost.TransparentBypassName
	}
	return bypass, nil
}
