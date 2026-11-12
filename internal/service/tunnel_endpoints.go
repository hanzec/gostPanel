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
		RelaySecret: rand.Text(), AllowlistNodeIDs: nodeIDs,
	}
	if err := s.setTunnelEndpoints(tunnel, req.EntryRuleID, req.ExitRuleID); err != nil {
		return nil, err
	}
	if err := setTunnelTargets(tunnel, req.Targets, req.UseClashRuleset, req.Strategy); err != nil {
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
	if entry.Node.ID == exit.Node.ID {
		return errors.ErrTunnelNodeSame
	}
	for _, endpoint := range []*model.GostRule{entry, exit} {
		bound, err := s.tunnelRepo.FindByEntranceID(endpoint.ID)
		if err == nil && bound.ID == tunnel.ID {
			continue
		}
		if err == nil || endpoint.Status == model.RuleStatusRunning {
			return errors.ErrTunnelEndpointInUse
		}
		if !stderrors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	tunnel.EntryRuleID, tunnel.ExitRuleID = &entryID, &exitID
	tunnel.EntryRule, tunnel.ExitRule = entry, exit
	tunnel.EntryNodeID, tunnel.ExitNodeID = entry.Node.ID, exit.Node.ID
	tunnel.EntryNode, tunnel.ExitNode = entry.Node, exit.Node
	tunnel.Protocol, tunnel.RelayPort = string(exit.Protocol), exit.ListenPort
	return nil
}

func setTunnelTargets(t *model.GostTunnel, targets []string, useClash bool, strategy string) error {
	if targets != nil || useClash || strategy != "" {
		t.Targets, t.UseClashRuleset, t.Strategy = targets, useClash, strategy
	} else if t.Strategy == "" {
		t.Targets, t.UseClashRuleset, t.Strategy = t.EntryRule.Targets, t.EntryRule.UseClashRuleset, t.EntryRule.Strategy
	}
	if t.Strategy == "" {
		t.Strategy = "round"
	}
	return validateTunnelTargets(t)
}

func validateTunnelTargets(t *model.GostTunnel) error {
	source := endpointTargetRule(t)
	if err := validateClashTarget(source.UseClashRuleset, source.Protocol, source.ListenIP, source.Targets); err != nil {
		return err
	}
	if !source.UseClashRuleset && len(source.Targets) == 0 {
		return errors.ErrTunnelTargetRequired
	}
	return nil
}

// Overlay tunnel targets for provisioning without changing the reusable endpoint.
func endpointTargetRule(t *model.GostTunnel) model.GostRule {
	rule := *t.EntryRule
	if t.Strategy != "" {
		rule.Targets, rule.UseClashRuleset, rule.Strategy = t.Targets, t.UseClashRuleset, t.Strategy
	}
	return rule
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
	if err := validateTunnelTargets(t); err != nil {
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
	redirect, err := prepareEndpointRedirect(t, entryClient, host)
	if err != nil {
		return err
	}
	if err := s.provisionEndpointTunnel(t, entryClient, exitClient, host, redirect); err != nil {
		s.rollbackEndpointTunnel(t, false)
		return err
	}
	// Persist both Gost listeners before enabling the redirect, which survives node restarts.
	if err := s.commitEndpointTunnel(t, entryClient, exitClient, redirect); err != nil {
		s.rollbackEndpointTunnel(t, true)
		return err
	}
	return nil
}

func prepareEndpointRedirect(t *model.GostTunnel, entryClient *gost.Client, host string) (*nodegrpc.TransparentRedirectRequest, error) {
	if !endpointTargetRule(t).UseClashRuleset {
		return nil, nil
	}
	redirect := transparentRedirectRequest(t.EntryRule, host)
	prepared, err := entryClient.ConfigureTransparentRedirect(context.Background(), redirect)
	if err != nil {
		logger.Warnf("准备隧道 %d 的入口透明分流失败: %v", t.ID, err)
		return nil, errors.ErrTunnelTransparentRedirect
	}
	redirect.Mark, redirect.DockerAddress = prepared.GetMark(), prepared.GetDockerAddress()
	return redirect, nil
}

func (s *TunnelService) provisionEndpointTunnel(t *model.GostTunnel, entryClient, exitClient *gost.Client, host string, redirect *nodegrpc.TransparentRedirectRequest) error {
	if err := s.provisionEndpointRelay(t, entryClient, exitClient, host); err != nil {
		return err
	}
	rules := NewRuleService(s.tunnelRepo.DB)
	mark := redirect.GetMark()
	source := endpointTargetRule(t)
	err := rules.provisionSelectedEntrance(entryClient, &source, t.EntryNode, t, nil, mark)
	t.EntryRule.ServiceID = source.ServiceID
	if err != nil {
		return err
	}
	if redirect.GetDockerAddress() != "" {
		svc := gost.BuildMarkedClashRedirectService(dockerEntranceServiceName(t.EntryRule.ID), mark)
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
	relay := &gost.ServiceConfig{
		Name: t.ServiceID, Addr: net.JoinHostPort(t.ExitRule.ListenIP, port),
		Listener: &gost.ListenerConfig{Type: t.Protocol},
		Handler:  &gost.HandlerConfig{Type: "relay", Auth: auth},
		Metadata: map[string]any{"enableStats": true},
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
	chain := newTunnelChain(t.ChainID, net.JoinHostPort(host, port), t.Protocol, auth)
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
			return errors.ErrTunnelTransparentRedirect
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
		return tx.Model(t).Updates(map[string]any{"service_id": t.ServiceID, "chain_id": t.ChainID, "status": model.TunnelStatusRunning}).Error
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
	useClash := endpointTargetRule(t).UseClashRuleset
	if save && useClash {
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
	if err := entryClient.DeleteService(serviceName); err != nil {
		return err
	}
	if useClash {
		if err := entryClient.DeleteService(dockerEntranceServiceName(t.EntryRule.ID)); err != nil {
			return err
		}
	}
	if err := stderrors.Join(entryClient.DeleteChain(tunnelChainName(t.ID)),
		entryClient.DeleteChain(clashTunnelChainName(t.ID)), exitClient.DeleteService(relayServiceName(t.ID))); err != nil {
		return err
	}
	if save {
		if err := stderrors.Join(entryClient.SaveConfig(), exitClient.SaveConfig()); err != nil {
			return errors.ErrTunnelEndpointSave
		}
	}
	if err := s.tunnelRepo.DB.Transaction(func(tx *gorm.DB) error {
		rules := repository.NewRuleRepository(tx)
		if err := rules.UpdateRuntime(t.EntryRule.ID, "", model.RuleStatusStopped); err != nil {
			return err
		}
		if err := tx.Model(&model.GostRule{}).Where("id = ?", t.ExitRule.ID).Update("status", model.RuleStatusStopped).Error; err != nil {
			return err
		}
		return repository.NewTunnelRepository(tx).UpdateStatus(t.ID, model.TunnelStatusStopped)
	}); err != nil {
		return err
	}
	t.EntryRule.ServiceID = ""
	t.EntryRule.Status = model.RuleStatusStopped
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
