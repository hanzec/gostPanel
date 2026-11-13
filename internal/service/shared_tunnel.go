package service

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Endpoint membership and its live Gost listeners are updated together.
var endpointTunnelRuntime sync.RWMutex

func (s *TunnelService) runningEndpointTunnels(column string, id, exclude uint) ([]*model.GostTunnel, error) {
	var tunnels []*model.GostTunnel
	err := s.tunnelRepo.DB.Where(column+" = ? AND id <> ? AND status = ?", id, exclude, model.TunnelStatusRunning).
		Order("id").Preload("EntryNode").Preload("ExitNode").Preload("EntryRule").Preload("ExitRule").Find(&tunnels).Error
	return tunnels, err
}

func (s *TunnelService) configureSharedRelay(client *gost.Client, relay *gost.ServiceConfig, tunnels []*model.GostTunnel) error {
	if err := s.allowlist.writeSharedRelayPolicy(client, relay.Name, tunnels); err != nil {
		return err
	}
	current, err := client.Service(relay.Name)
	if err != nil {
		return err
	}
	if current != nil && current.Handler != nil && current.Handler.Auther == relay.Name && current.Bypass == relay.Bypass {
		return nil
	}
	address, err := client.ObserverAddress()
	if err != nil {
		return err
	}
	if err := client.EnsureRelayAuther(relay.Name, address, client.ObserverToken()); err != nil {
		return err
	}
	relay.Handler.Auth = nil
	relay.Handler.Auther = relay.Name
	return client.ReplaceService(relay)
}

func (s *GostAllowlistService) writeSharedRelayPolicy(client *gost.Client, name string, tunnels []*model.GostTunnel) error {
	policy := nodegrpc.RelayAuthConfig{Users: make([]nodegrpc.RelayAuthUser, 0, len(tunnels))}
	for _, tunnel := range tunnels {
		addresses, restrict, err := s.relaySources(tunnel)
		if err != nil {
			return err
		}
		user := nodegrpc.RelayAuthUser{
			Username: relayServiceName(tunnel.ID), Password: tunnel.RelaySecret,
			RestrictSources: restrict, AllowedIPs: addresses,
		}
		policy.Users = append(policy.Users, user)
	}
	err := client.ConfigureRelayAuth(name, policy)
	if status.Code(err) == codes.Unimplemented {
		return bizerrors.ErrTunnelSharedDaemon
	}
	return err
}

func (s *TunnelService) removeEndpointRelay(client *gost.Client, tunnel *model.GostTunnel, peers []*model.GostTunnel) error {
	if len(peers) == 0 {
		name := tunnel.ServiceID
		if name == "" {
			name = relayServiceName(tunnel.ID)
		}
		if tunnel.ExitRule.Role != model.EndpointRoleExit {
			return client.DeleteService(name)
		}
		shared, err := client.RelayAuthConfigured(name)
		if err != nil {
			return err
		}
		if err := client.DeleteService(name); err != nil || !shared {
			return err
		}
		return client.DeleteRelayAuther(name)
	}
	name := peers[0].ServiceID
	current, err := client.Service(name)
	if err != nil {
		return err
	}
	if current == nil || current.Handler == nil || current.Handler.Auther != name {
		// Rollback can arrive before the shared auth plugin was applied.
		return nil
	}
	return s.allowlist.writeSharedRelayPolicy(client, name, peers)
}

func (s *TunnelService) applySharedIngress(client *gost.Client, t *model.GostTunnel, tunnels []*model.GostTunnel, redirect *nodegrpc.TransparentRedirectRequest) error {
	if redirect == nil {
		return s.applySharedFixedIngress(client, t.EntryRule, tunnels)
	}
	rules := NewRuleService(s.tunnelRepo.DB)
	for _, tunnel := range tunnels {
		source := endpointTargetRule(tunnel)
		if _, err := rules.prepareTransparentBypass(client, transparentPolicyName(tunnel.ID), &source); err != nil {
			return err
		}
	}
	return s.applySharedTransparentIngress(client, t.EntryRule, tunnels, redirect)
}

func (s *TunnelService) applySharedTransparentIngress(client *gost.Client, endpoint *model.GostRule, tunnels []*model.GostTunnel, redirect *nodegrpc.TransparentRedirectRequest) error {
	group, sniff := sharedTransparentGroup(tunnels)
	for _, tunnel := range tunnels {
		// The group evaluates the policy before selecting the marked route.
		if err := client.EnsureMarkedTransparentChain(tunnel.ChainID, sharedTransparentChainName(tunnel.ID), redirect.GetMark(), ""); err != nil {
			return err
		}
	}
	rules := NewRuleService(s.tunnelRepo.DB)
	listener := gost.BuildMarkedTransparentRedirectService(entranceServiceName(endpoint.ID), redirect.GetMark(), sniff)
	listener.Addr = net.JoinHostPort(endpoint.ListenIP, strconv.Itoa(endpoint.ListenPort))
	listener.Handler.ChainGroup = group
	if err := rules.setupRuleObserver(client, endpoint, listener); err != nil {
		return err
	}
	if err := client.ReplaceService(listener); err != nil {
		if status.Code(err) == codes.InvalidArgument || status.Code(err) == codes.Unimplemented {
			return bizerrors.ErrTunnelSharedDaemon
		}
		return err
	}
	endpoint.ServiceID = listener.Name

	if redirect.GetDockerAddress() == "" {
		return nil
	}
	docker := gost.BuildMarkedTransparentRedirectService(dockerEntranceServiceName(endpoint.ID), redirect.GetMark(), sniff)
	docker.Addr = net.JoinHostPort(redirect.GetDockerAddress(), strconv.Itoa(endpoint.ListenPort))
	docker.Listener.Metadata = map[string]any{"tproxy": true}
	docker.Handler.ChainGroup = group
	return client.ReplaceService(docker)
}

func sharedTransparentGroup(tunnels []*model.GostTunnel) (*gost.ChainGroupConfig, bool) {
	var matches []string
	group := &gost.ChainGroupConfig{Selector: &gost.SelectorConfig{Strategy: "round"}}
	for _, tunnel := range tunnels {
		source := endpointTargetRule(tunnel)
		entry := gost.ChainGroupEntry{Chain: sharedTransparentChainName(tunnel.ID)}
		if transparentFiltering(&source) {
			name := transparentPolicyName(tunnel.ID)
			if source.UseClashRuleset && !hasSelectedRules(&source) {
				name = gost.TransparentBypassName
			}
			rule := fmt.Sprintf("Bypass(%q)", name)
			matches = append(matches, rule)
			entry.Matcher = &gost.ChainMatcher{Rule: rule}
		}
		group.Chains = append(group.Chains, entry)
	}
	if len(matches) != 0 {
		fallback := &gost.ChainMatcher{Rule: "!(" + strings.Join(matches, " || ") + ")"}
		for i, entry := range group.Chains {
			if entry.Matcher == nil {
				group.Chains[i].Matcher = fallback
			}
		}
	}
	return group, len(matches) != 0
}

func sharedTransparentChainName(id uint) string {
	return fmt.Sprintf("tunnel-%d-shared-chain", id)
}

func (s *TunnelService) restoreEndpointIngress(client *gost.Client, peers []*model.GostTunnel) error {
	first := peers[0]
	source := endpointTargetRule(first)
	if len(peers) == 1 {
		current, err := client.Service(entranceServiceName(first.EntryRule.ID))
		if err != nil {
			return err
		}
		if current != nil && current.Handler != nil && current.Handler.Chain != "" {
			// A failed start may leave the original single-tunnel listener untouched.
			if source.UseOriginalTarget || source.UseClashRuleset {
				docker, err := client.Service(dockerEntranceServiceName(first.EntryRule.ID))
				if err != nil {
					return err
				}
				if docker == nil || docker.Handler == nil || docker.Handler.ChainGroup == nil {
					return client.DeleteChain(sharedTransparentChainName(first.ID))
				}
			} else {
				return client.DeleteService(sharedFixedIngressServiceName(first.EntryRule.ID, first.ID))
			}
		}
	}
	if source.UseOriginalTarget || source.UseClashRuleset {
		redirect, err := prepareEndpointRedirect(first, client, relayHost(first.ExitRule, first.ExitNode), peers[1:])
		if err != nil {
			return err
		}
		if err := s.applySharedTransparentIngress(client, first.EntryRule, peers, redirect); err != nil {
			return err
		}
		redirect.RelayHosts = relayHosts(peers)
		redirect.Action = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY
		_, err = client.ConfigureTransparentRedirect(context.Background(), redirect)
		return err
	}
	return s.applySharedFixedIngress(client, first.EntryRule, peers)
}

func validateSharedIngress(tunnel *model.GostTunnel, peers []*model.GostTunnel) error {
	source := endpointTargetRule(tunnel)
	transparent := source.UseOriginalTarget || source.UseClashRuleset
	for _, peer := range peers {
		rule := endpointTargetRule(peer)
		if transparent != (rule.UseOriginalTarget || rule.UseClashRuleset) {
			return bizerrors.ErrTunnelSharedIngressPolicy
		}
	}
	return nil
}

func relayHosts(tunnels []*model.GostTunnel) []string {
	hosts := make([]string, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if host := relayHost(tunnel.ExitRule, tunnel.ExitNode); host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}
