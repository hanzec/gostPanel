package service

import (
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
	if stderrors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (entry.Type != model.RuleTypeForward || entry.Node == nil || (!entry.UseClashRuleset && len(entry.Targets) == 0))) {
		return errors.ErrTunnelEntryInvalid
	}
	if err != nil {
		return err
	}
	exit, err := rules.FindByID(exitID)
	if stderrors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (exit.Type != model.RuleTypeForward || exit.Node == nil || exit.UseClashRuleset || len(exit.Targets) != 0 || !relayBindReachable(exit.ListenIP))) {
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
	if err := endpointNodesOnline(t); err != nil {
		return err
	}
	if relayHost(t, t.ExitNode.Address) == "" {
		return errors.ErrExtractHostFailed
	}
	if err := s.applyEndpointTunnel(t); err != nil {
		if cleanupErr := s.stopEndpointTunnel(t); cleanupErr != nil {
			t.Status = model.TunnelStatusError
			_ = s.tunnelRepo.UpdateStatus(t.ID, t.Status)
			logger.Warnf("清理隧道 %d 的部分配置失败: %v", t.ID, cleanupErr)
		}
		return err
	}
	return nil
}

func (s *TunnelService) applyEndpointTunnel(t *model.GostTunnel) error {
	entryClient, exitClient := utils.GetGostClient(t.EntryNode), utils.GetGostClient(t.ExitNode)
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
	chain := newTunnelChain(t.ChainID, net.JoinHostPort(relayHost(t, t.ExitNode.Address), port), t.Protocol, auth)
	if err := entryClient.CreateChain(chain); err != nil {
		return errors.ErrTunnelChainCreateFailed
	}
	if err := NewRuleService(s.tunnelRepo.DB).startSelectedEntrance(t.EntryRule, t.EntryNode, t, nil); err != nil {
		return err
	}
	if err := exitClient.SaveConfig(); err != nil {
		logger.Warnf("保存隧道 %d 出口配置失败: %v", t.ID, err)
		return errors.ErrTunnelEndpointSave
	}
	if err := s.tunnelRepo.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.GostRule{}).Where("id = ?", t.ExitRule.ID).Updates(map[string]any{"service_id": t.ServiceID, "status": model.RuleStatusRunning}).Error; err != nil {
			return err
		}
		return tx.Model(t).Updates(map[string]any{"service_id": t.ServiceID, "chain_id": t.ChainID, "status": model.TunnelStatusRunning}).Error
	}); err != nil {
		return err
	}
	t.Status = model.TunnelStatusRunning
	return nil
}

func (s *TunnelService) stopEndpointTunnel(t *model.GostTunnel) error {
	if t.EntryRule == nil || t.ExitRule == nil {
		return errors.ErrTunnelEndpointsRequired
	}
	if err := endpointNodesOnline(t); err != nil {
		return err
	}
	entryClient, exitClient := utils.GetGostClient(t.EntryNode), utils.GetGostClient(t.ExitNode)
	// Remove the entrance before its chain so a live transparent listener can't fall back to direct.
	if err := NewRuleService(s.tunnelRepo.DB).stopSelectedEntrance(t.EntryRule); err != nil {
		return err
	}
	if err := stderrors.Join(entryClient.DeleteChain(tunnelChainName(t.ID)),
		entryClient.DeleteChain(clashTunnelChainName(t.ID)), exitClient.DeleteService(relayServiceName(t.ID))); err != nil {
		return err
	}
	if err := stderrors.Join(entryClient.SaveConfig(), exitClient.SaveConfig()); err != nil {
		return errors.ErrTunnelEndpointSave
	}
	if err := s.tunnelRepo.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.GostRule{}).Where("id = ?", t.ExitRuleID).Update("status", model.RuleStatusStopped).Error; err != nil {
			return err
		}
		return repository.NewTunnelRepository(tx).UpdateStatus(t.ID, model.TunnelStatusStopped)
	}); err != nil {
		return err
	}
	t.Status = model.TunnelStatusStopped
	return nil
}

func relayHost(t *model.GostTunnel, detected string) string {
	if t.ExitRule != nil && t.ExitRule.ListenIP != "" {
		return t.ExitRule.ListenIP
	}
	return detected
}

func (s *TunnelService) recordTunnelAction(t *model.GostTunnel, action, label, user, ip, agent string) {
	s.logService.Record(user, action, model.ResourceTypeTunnel, t.ID, fmt.Sprintf("%s隧道: %s", label, t.Name), ip, agent)
}
