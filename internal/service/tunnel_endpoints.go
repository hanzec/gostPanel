package service

import (
	"context"
	"crypto/rand"
	stderrors "errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"yagp/internal/dto"
	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *TunnelService) createEndpointTunnel(req *dto.CreateTunnelReq, nodeIDs []uint, username, ip, userAgent string) (*model.GostTunnel, error) {
	tunnel := &model.GostTunnel{
		Name: req.Name, Remark: req.Remark, Status: model.TunnelStatusStopped,
		RelaySecret: rand.Text(), AllowlistNodeIDs: nodeIDs, Protocol: req.Protocol,
		AutoAllowlist: req.AllowlistNodeIDs == nil,
	}
	if err := s.setTunnelEndpoints(tunnel, req.EntryRuleID, req.ExitRuleID); err != nil {
		return nil, err
	}
	if tunnel.ExitRule.Role != model.EndpointRoleExit {
		if err := validateRelayProtocolOptions(tunnel.Protocol, req.ProtocolOptions); err != nil {
			return nil, err
		}
		tunnel.ProtocolOptions = req.ProtocolOptions
	}
	var useOriginal *bool
	if req.UseOriginalTarget {
		useOriginal = &req.UseOriginalTarget
	}
	if err := s.setTunnelTargets(tunnel, req.Targets, useOriginal, req.UseClashRuleset, req.RulesetIDs, req.ManualDomains, req.Strategy); err != nil {
		return nil, err
	}
	if err := endpointNodesOnline(tunnel); err != nil {
		return nil, err
	}
	if err := s.tunnelRepo.Create(tunnel); err != nil {
		return nil, err
	}
	if err := s.startEndpointTunnel(tunnel); err != nil {
		// Keep the association if remote cleanup could not be confirmed, so it can be retried.
		if tunnel.Status != model.TunnelStatusError {
			_ = s.tunnelRepo.Delete(tunnel.ID)
		}
		return nil, err
	}
	s.recordTunnelAction(tunnel, model.ActionCreate, "创建并启动", username, ip, userAgent)
	return s.tunnelRepo.FindByID(tunnel.ID)
}

func (s *TunnelService) setTunnelEndpoints(tunnel *model.GostTunnel, entryID, exitID uint) error {
	if entryID == 0 || exitID == 0 || entryID == exitID {
		return errors.ErrTunnelEndpointsRequired
	}
	rules := repository.NewRuleRepository(s.tunnelRepo.DB)
	entry, err := rules.FindByID(entryID)
	if stderrors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (entry.Type != model.RuleTypeForward || entry.Node == nil)) {
		return errors.ErrTunnelEntryInvalid
	}
	if err != nil {
		return err
	}
	exit, err := rules.FindByID(exitID)
	if stderrors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (exit.Type != model.RuleTypeForward || exit.Node == nil || !relayBindReachable(exit.ListenIP))) {
		return errors.ErrTunnelExitInvalid
	}
	if err != nil {
		return err
	}
	if err := validateTunnelEndpointRoles(entry, exit); err != nil {
		return err
	}
	if entry.Node.ID == exit.Node.ID {
		return errors.ErrTunnelNodeSame
	}
	for _, endpoint := range []*model.GostRule{entry, exit} {
		bindings, err := rules.EntranceBindings([]uint{endpoint.ID})
		if err != nil {
			return err
		}
		if len(bindings) == 0 && endpoint.Status == model.RuleStatusRunning {
			return errors.ErrTunnelEndpointInUse
		}
		for _, bound := range bindings {
			if bound.ID == tunnel.ID {
				continue
			}
			if endpoint.Role == "" {
				return errors.ErrTunnelEndpointInUse
			}
			if bound.EntryRuleID != nil && *bound.EntryRuleID == entryID && bound.ExitRuleID != nil && *bound.ExitRuleID == exitID {
				return errors.ErrTunnelDuplicateEndpoints
			}
		}
	}
	tunnel.EntryRuleID, tunnel.ExitRuleID = &entryID, &exitID
	tunnel.EntryRule, tunnel.ExitRule = entry, exit
	tunnel.EntryNodeID, tunnel.ExitNodeID = entry.Node.ID, exit.Node.ID
	tunnel.EntryNode, tunnel.ExitNode = entry.Node, exit.Node
	// Explicit exit endpoints own the transport; older clients may still store it on the tunnel.
	if exit.Role == model.EndpointRoleExit || tunnel.Protocol == "" {
		tunnel.Protocol = string(exit.Protocol)
	}
	if exit.Role == model.EndpointRoleExit {
		tunnel.ProtocolOptions = nil
	}
	tunnel.RelayPort = exit.ListenPort
	return nil
}

func (s *TunnelService) setTunnelTargets(t *model.GostTunnel, targets []string, useOriginal *bool, useClash bool, rulesetIDs []uint, manualDomains []string, strategy string) error {
	if t.ExitRule.Role == model.EndpointRoleExit {
		// Ignore obsolete tunnel-level input: the selected exit is the only source of target policy.
		t.Targets, t.UseClashRuleset, t.Strategy = nil, false, ""
		t.UseOriginalTarget, t.RulesetIDs, t.ManualDomains = false, nil, nil
		return validateTunnelTargets(t)
	}
	if targets != nil || useOriginal != nil || useClash || rulesetIDs != nil || manualDomains != nil || strategy != "" {
		t.Targets, t.UseClashRuleset, t.Strategy = targets, useClash, strategy
		if useOriginal != nil {
			t.UseOriginalTarget = *useOriginal
		}
		if rulesetIDs != nil {
			t.RulesetIDs = rulesetIDs
		}
		if manualDomains != nil {
			t.ManualDomains = manualDomains
		}
	} else if t.Strategy == "" {
		t.Targets, t.UseClashRuleset, t.Strategy = t.EntryRule.Targets, t.EntryRule.UseClashRuleset, t.EntryRule.Strategy
		t.UseOriginalTarget, t.RulesetIDs, t.ManualDomains = t.EntryRule.UseOriginalTarget, t.EntryRule.RulesetIDs, t.EntryRule.ManualDomains
	}
	if !t.UseOriginalTarget && (len(t.RulesetIDs) != 0 || len(t.ManualDomains) != 0) {
		return errors.ErrEndpointTargets
	}
	if rulesetIDs != nil || manualDomains != nil {
		ids, domains, err := NewGostRulesetService(s.tunnelRepo.DB).NormalizeSelection(context.Background(), t.RulesetIDs, t.ManualDomains)
		if err != nil {
			return err
		}
		t.RulesetIDs, t.ManualDomains = ids, domains
	}
	if t.Strategy == "" {
		t.Strategy = "round"
	}
	return validateTunnelTargets(t)
}

func validateTunnelTargets(t *model.GostTunnel) error {
	source := endpointTargetRule(t)
	if err := validateTransparentTarget(source.UseOriginalTarget, source.UseClashRuleset, source.Protocol, source.ListenIP, source.Targets); err != nil {
		return err
	}
	if source.Protocol == model.RuleProtocolTransparent && !source.UseOriginalTarget && !source.UseClashRuleset {
		return errors.ErrRuleTransparentTargetRequired
	}
	if !source.UseOriginalTarget && !source.UseClashRuleset && len(source.Targets) == 0 {
		return errors.ErrTunnelTargetRequired
	}
	return nil
}

// Apply the exit policy to the ingress listener without changing either reusable endpoint.
func endpointTargetRule(t *model.GostTunnel) model.GostRule {
	rule := *t.EntryRule
	if t.ExitRule != nil && t.ExitRule.Role == model.EndpointRoleExit {
		rule.Targets, rule.UseClashRuleset, rule.Strategy = t.ExitRule.Targets, t.ExitRule.UseClashRuleset, t.ExitRule.Strategy
		rule.UseOriginalTarget, rule.RulesetIDs, rule.ManualDomains = t.ExitRule.UseOriginalTarget, t.ExitRule.RulesetIDs, t.ExitRule.ManualDomains
	} else if t.Strategy != "" {
		rule.Targets, rule.UseClashRuleset, rule.Strategy = t.Targets, t.UseClashRuleset, t.Strategy
		rule.UseOriginalTarget, rule.RulesetIDs, rule.ManualDomains = t.UseOriginalTarget, t.RulesetIDs, t.ManualDomains
	}
	return rule
}

func validateTunnelEndpointRoles(entry, exit *model.GostRule) error {
	if entry.Role != "" && entry.Role != model.EndpointRoleEntry {
		return errors.ErrTunnelEntryInvalid
	}
	if exit.Role != "" && exit.Role != model.EndpointRoleExit {
		return errors.ErrTunnelExitInvalid
	}
	return nil
}

func relayBindReachable(ip string) bool {
	if ip == "" {
		return true
	}
	addr, err := netip.ParseAddr(ip)
	return err == nil && addr.IsGlobalUnicast()
}

func endpointNodesOnline(t *model.GostTunnel) error {
	if t.EntryNode == nil || t.EntryNode.Status != model.NodeStatusOnline {
		return errors.ErrEntryNodeOffline
	}
	if t.ExitNode == nil || t.ExitNode.Status != model.NodeStatusOnline {
		return errors.ErrExitNodeOffline
	}
	return nil
}

func (s *TunnelService) startEndpointTunnel(t *model.GostTunnel) error {
	if t.EntryRule == nil || t.ExitRule == nil {
		return errors.ErrTunnelEndpointsRequired
	}
	if err := validateTunnelEndpointRoles(t.EntryRule, t.ExitRule); err != nil {
		return err
	}
	if t.ExitRule.Role == model.EndpointRoleExit {
		t.Protocol, t.RelayPort = string(t.ExitRule.Protocol), t.ExitRule.ListenPort
		t.ProtocolOptions = nil
	}
	if err := validateTunnelTargets(t); err != nil {
		return err
	}
	peers, err := s.runningEndpointTunnels("entry_rule_id", t.EntryRule.ID, t.ID)
	if err != nil {
		return err
	}
	if err := validateSharedIngress(t, peers); err != nil {
		return err
	}
	if err := endpointNodesOnline(t); err != nil {
		return err
	}
	host := relayHost(t.ExitRule, t.ExitNode)
	if host == "" {
		return errors.ErrExtractHostFailed
	}
	if t.Status == model.TunnelStatusError {
		if err := s.stopEndpointTunnel(t); err != nil {
			return err
		}
	}
	entryClient, exitClient := utils.GetGostClient(t.EntryNode), utils.GetGostClient(t.ExitNode)
	redirect, err := prepareEndpointRedirect(t, entryClient, host, peers)
	if err != nil {
		return err
	}
	if err := s.provisionEndpointTunnel(t, entryClient, exitClient, host, redirect, peers); err != nil {
		// A shared listener may have been changed while the other tunnels were already persisted.
		s.rollbackEndpointTunnel(t, len(peers) != 0 && redirect != nil)
		return err
	}
	// Persist both Gost listeners before enabling the redirect, which survives node restarts.
	if err := s.commitEndpointTunnel(t, entryClient, exitClient, redirect); err != nil {
		s.rollbackEndpointTunnel(t, true)
		return err
	}
	return nil
}

func prepareEndpointRedirect(t *model.GostTunnel, entryClient *gost.Client, host string, peers []*model.GostTunnel) (*nodegrpc.TransparentRedirectRequest, error) {
	source := endpointTargetRule(t)
	if !source.UseOriginalTarget && !source.UseClashRuleset {
		return nil, nil
	}
	redirect := transparentRedirectRequest(t.EntryRule, host)
	if len(peers) != 0 {
		redirect.RelayHosts = relayHosts(append(peers, t))
	}
	prepared, err := entryClient.ConfigureTransparentRedirect(context.Background(), redirect)
	if err != nil {
		logger.Warnf("准备隧道 %d 的入口透明分流失败: %v", t.ID, err)
		return nil, transparentRedirectError(err)
	}
	redirect.Mark, redirect.DockerAddress = prepared.GetMark(), prepared.GetDockerAddress()
	if len(peers) != 0 && !prepared.GetMultiRelaySupported() {
		return nil, errors.ErrTunnelSharedDaemon
	}
	return redirect, nil
}

func (s *TunnelService) provisionEndpointTunnel(t *model.GostTunnel, entryClient, exitClient *gost.Client, host string, redirect *nodegrpc.TransparentRedirectRequest, peers []*model.GostTunnel) error {
	if err := s.provisionEndpointRelay(t, entryClient, exitClient, host); err != nil {
		return err
	}
	if len(peers) != 0 {
		return s.applySharedIngress(entryClient, t, append(peers, t), redirect)
	}
	return s.provisionEndpointIngress(t, entryClient, redirect)
}

func (s *TunnelService) provisionEndpointIngress(t *model.GostTunnel, entryClient *gost.Client, redirect *nodegrpc.TransparentRedirectRequest) error {
	rules := NewRuleService(s.tunnelRepo.DB)
	mark := redirect.GetMark()
	source := endpointTargetRule(t)
	err := rules.provisionSelectedEntrance(entryClient, &source, t.EntryNode, t, nil, mark)
	t.EntryRule.ServiceID = source.ServiceID
	if err != nil {
		return err
	}
	if redirect.GetDockerAddress() != "" {
		svc := gost.BuildMarkedTransparentRedirectService(dockerEntranceServiceName(t.EntryRule.ID), mark, transparentFiltering(&source))
		svc.Addr = net.JoinHostPort(redirect.GetDockerAddress(), strconv.Itoa(t.EntryRule.ListenPort))
		// IP_TRANSPARENT lets Gost start before the bridge address is assigned. The handler reads SO_ORIGINAL_DST for DNAT.
		svc.Listener.Metadata = map[string]any{"tproxy": true}
		svc.Handler.Chain = clashTunnelChainName(t.ID)
		if _, err := createServiceWithTrafficQuota(s.nodeRepo, entryClient, *t.EntryNode, svc); err != nil {
			logger.Warnf("创建隧道 %d 的 Docker 入口监听失败: %v", t.ID, err)
			return errors.ErrRuleStartFailed
		}
	}
	return nil
}

func (s *TunnelService) rollbackEndpointTunnel(t *model.GostTunnel, saved bool) {
	if cleanupErr := s.cleanupEndpointTunnel(t, saved); cleanupErr != nil {
		if t.EntryRule.ServiceID != "" {
			_ = repository.NewRuleRepository(s.tunnelRepo.DB).UpdateRuntime(t.EntryRule.ID, t.EntryRule.ServiceID, model.RuleStatusError)
		}
		t.Status = model.TunnelStatusError
		_ = s.tunnelRepo.UpdateStatus(t.ID, t.Status)
		logger.Warnf("清理隧道 %d 的部分配置失败: %v", t.ID, cleanupErr)
	}
}

func (s *TunnelService) provisionEndpointRelay(t *model.GostTunnel, entryClient, exitClient *gost.Client, host string) error {
	t.ServiceID, t.ChainID = relayServiceName(t.ID), tunnelChainName(t.ID)
	auth := &gost.AuthConfig{Username: t.ServiceID, Password: t.RelaySecret}
	port := strconv.Itoa(t.RelayPort)
	options := t.ProtocolOptions
	if t.ExitRule.Role == model.EndpointRoleExit {
		options = t.ExitRule.ProtocolOptions
	}
	relay := &gost.ServiceConfig{
		Name: t.ServiceID, Addr: net.JoinHostPort(t.ExitRule.ListenIP, port),
		Listener: gost.RelayListener(t.Protocol, options),
		Handler:  &gost.HandlerConfig{Type: "relay", Auth: auth},
		Metadata: map[string]any{"enableStats": true},
	}
	peers, err := s.runningEndpointTunnels("exit_rule_id", t.ExitRule.ID, t.ID)
	if err != nil {
		return err
	}
	if len(peers) != 0 {
		t.ServiceID, relay.Name = peers[0].ServiceID, peers[0].ServiceID
		if err := s.configureSharedRelay(exitClient, relay, append(peers, t)); err != nil {
			return err
		}
		return s.createEndpointRelayChain(t, entryClient, host, auth, options)
	}
	if err := s.allowlist.prepareRelay(exitClient, relay, t); err != nil {
		return err
	}
	created, err := createServiceWithTrafficQuota(s.nodeRepo, exitClient, *t.ExitNode, relay)
	if err != nil {
		if stderrors.Is(err, errors.ErrNodeTrafficQuotaUnavailable) {
			return err
		}
		return errors.ErrTunnelRelayCreateFailed
	}
	if !created {
		if _, err := exitClient.SetRelayAllowlist(relay.Name, relay.Admission != ""); err != nil {
			return err
		}
	}
	return s.createEndpointRelayChain(t, entryClient, host, auth, options)
}

func (s *TunnelService) createEndpointRelayChain(t *model.GostTunnel, entryClient *gost.Client, host string, auth *gost.AuthConfig, options map[string]string) error {
	chain := newTunnelChain(t.ChainID, net.JoinHostPort(host, strconv.Itoa(t.RelayPort)), t.Protocol, options, auth)
	if err := entryClient.CreateChain(chain); err != nil {
		return errors.ErrTunnelChainCreateFailed
	}
	return nil
}

func (s *TunnelService) commitEndpointTunnel(t *model.GostTunnel, entryClient, exitClient *gost.Client, redirect *nodegrpc.TransparentRedirectRequest) error {
	if err := entryClient.SaveConfig(); err != nil {
		logger.Warnf("保存隧道 %d 入口配置失败: %v", t.ID, err)
		return errors.ErrRuleStartFailed
	}
	if err := exitClient.SaveConfig(); err != nil {
		logger.Warnf("保存隧道 %d 出口配置失败: %v", t.ID, err)
		return errors.ErrTunnelEndpointSave
	}
	if redirect != nil {
		redirect.Action = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY
		if _, err := entryClient.ConfigureTransparentRedirect(context.Background(), redirect); err != nil {
			logger.Warnf("应用隧道 %d 的入口透明分流失败: %v", t.ID, err)
			return transparentRedirectError(err)
		}
	}
	if err := s.tunnelRepo.DB.Transaction(func(tx *gorm.DB) error {
		rules := repository.NewRuleRepository(tx)
		if err := rules.UpdateRuntime(t.EntryRule.ID, t.EntryRule.ServiceID, model.RuleStatusRunning); err != nil {
			return err
		}
		if err := rules.UpdateRuntime(t.ExitRule.ID, t.ServiceID, model.RuleStatusRunning); err != nil {
			return err
		}
		return tx.Model(t).Select("ServiceID", "ChainID", "Protocol", "ProtocolOptions", "RelayPort", "Status").Updates(&model.GostTunnel{
			ServiceID: t.ServiceID, ChainID: t.ChainID, Protocol: t.Protocol, ProtocolOptions: t.ProtocolOptions,
			RelayPort: t.RelayPort, Status: model.TunnelStatusRunning,
		}).Error
	}); err != nil {
		return err
	}
	t.EntryRule.Status = model.RuleStatusRunning
	t.Status = model.TunnelStatusRunning
	return nil
}

func (s *TunnelService) stopEndpointTunnel(t *model.GostTunnel) error {
	return s.cleanupEndpointTunnel(t, true)
}

func (s *TunnelService) cleanupEndpointTunnel(t *model.GostTunnel, save bool) error {
	if t.EntryRule == nil || t.ExitRule == nil {
		return errors.ErrTunnelEndpointsRequired
	}
	if err := endpointNodesOnline(t); err != nil {
		return err
	}
	entryClient, exitClient := utils.GetGostClient(t.EntryNode), utils.GetGostClient(t.ExitNode)
	entryPeers, err := s.runningEndpointTunnels("entry_rule_id", t.EntryRule.ID, t.ID)
	if err != nil {
		return err
	}
	exitPeers, err := s.runningEndpointTunnels("exit_rule_id", t.ExitRule.ID, t.ID)
	if err != nil {
		return err
	}
	source := endpointTargetRule(t)
	transparent := source.UseOriginalTarget || source.UseClashRuleset
	if len(entryPeers) > 0 {
		if err := s.restoreEndpointIngress(entryClient, entryPeers); err != nil {
			return err
		}
	} else if save && transparent {
		request := &nodegrpc.TransparentRedirectRequest{
			Action: nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_REMOVE,
			Owner:  entranceServiceName(t.EntryRule.ID),
		}
		if _, err := entryClient.ConfigureTransparentRedirect(context.Background(), request); err != nil && status.Code(err) != codes.Unimplemented {
			logger.Warnf("移除隧道 %d 的入口透明分流失败: %v", t.ID, err)
			return errors.ErrTunnelTransparentRedirect
		}
	}
	// Remove the entrance before its chain so a live transparent listener can't fall back to direct.
	serviceName := t.EntryRule.ServiceID
	if serviceName == "" {
		serviceName = entranceServiceName(t.EntryRule.ID)
	}
	if len(entryPeers) == 0 {
		if err := entryClient.DeleteService(serviceName); err != nil {
			return err
		}
		if transparent {
			if err := entryClient.DeleteService(dockerEntranceServiceName(t.EntryRule.ID)); err != nil {
				return err
			}
		}
	}
	if !transparent && (len(entryPeers) != 0 || t.EntryRule.Role == model.EndpointRoleEntry) {
		if err := entryClient.DeleteService(sharedFixedIngressServiceName(t.EntryRule.ID, t.ID)); err != nil {
			return err
		}
	}
	cleanupErr := stderrors.Join(entryClient.DeleteChain(tunnelChainName(t.ID)),
		entryClient.DeleteChain(clashTunnelChainName(t.ID)), s.removeEndpointRelay(exitClient, t, exitPeers))
	if transparent {
		cleanupErr = stderrors.Join(cleanupErr, entryClient.DeleteChain(sharedTransparentChainName(t.ID)))
	}
	if len(source.RulesetIDs) != 0 || len(source.ManualDomains) != 0 {
		cleanupErr = stderrors.Join(cleanupErr, entryClient.DeleteNamedTransparentBypass(transparentPolicyName(t.ID)))
	}
	if err := cleanupErr; err != nil {
		return err
	}
	if save {
		if err := stderrors.Join(entryClient.SaveConfig(), exitClient.SaveConfig()); err != nil {
			return errors.ErrTunnelEndpointSave
		}
	}
	if err := s.tunnelRepo.DB.Transaction(func(tx *gorm.DB) error {
		rules := repository.NewRuleRepository(tx)
		if len(entryPeers) == 0 {
			if err := rules.UpdateRuntime(t.EntryRule.ID, "", model.RuleStatusStopped); err != nil {
				return err
			}
		}
		if len(exitPeers) == 0 {
			if err := rules.UpdateRuntime(t.ExitRule.ID, "", model.RuleStatusStopped); err != nil {
				return err
			}
		}
		return repository.NewTunnelRepository(tx).UpdateStatus(t.ID, model.TunnelStatusStopped)
	}); err != nil {
		return err
	}
	if len(entryPeers) == 0 {
		t.EntryRule.ServiceID = ""
		t.EntryRule.Status = model.RuleStatusStopped
	}
	t.Status = model.TunnelStatusStopped
	return nil
}

func transparentRedirectRequest(rule *model.GostRule, host string) *nodegrpc.TransparentRedirectRequest {
	return &nodegrpc.TransparentRedirectRequest{
		Action:    nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_PREPARE,
		Owner:     entranceServiceName(rule.ID),
		Port:      uint32(rule.ListenPort),
		RelayHost: host,
	}
}

func dockerEntranceServiceName(id uint) string {
	return entranceServiceName(id) + "-docker"
}

func relayHost(exitRule *model.GostRule, exitNode *model.GostNode) string {
	if exitRule != nil && exitRule.ListenIP != "" {
		if exitNode != nil {
			for _, mapping := range exitNode.AddressMappings {
				if mapping.ListenIP == exitRule.ListenIP {
					return mapping.ConnectIP
				}
			}
		}
		return exitRule.ListenIP
	}
	if exitNode == nil {
		return ""
	}
	return exitNode.Address
}

func (s *TunnelService) recordTunnelAction(t *model.GostTunnel, action, label, user, ip, agent string) {
	s.logService.Record(user, action, model.ResourceTypeTunnel, t.ID, fmt.Sprintf("%s隧道: %s", label, t.Name), ip, agent)
}
