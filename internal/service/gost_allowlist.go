package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"

	"gorm.io/gorm"
)

type GostAllowlistService struct {
	db *gorm.DB
}

func NewGostAllowlistService(db *gorm.DB) *GostAllowlistService {
	return &GostAllowlistService{db: db}
}

func (s *GostAllowlistService) validateNodeIDs(ctx context.Context, nodeIDs []uint) ([]uint, error) {
	nodeIDs = append([]uint{}, nodeIDs...)
	slices.Sort(nodeIDs)
	nodeIDs = slices.Compact(nodeIDs)
	_, err := s.resolveAddresses(ctx, nodeIDs, true)
	return nodeIDs, err
}

func (s *GostAllowlistService) resolveAddresses(ctx context.Context, nodeIDs []uint, strict bool) ([]string, error) {
	addresses := []string{}
	if len(nodeIDs) == 0 {
		return addresses, nil
	}
	var nodes []model.GostNode
	if err := s.db.WithContext(ctx).Select("id", "name", "ipv4", "ipv6", "addresses", "manual_addresses").Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
		return nil, err
	}
	if strict && len(nodes) != len(nodeIDs) {
		return nil, bizerrors.New(40000, "白名单包含不存在的节点", http.StatusBadRequest)
	}
	for _, node := range nodes {
		addresses = append(addresses, node.ManualAddresses...)
		if len(node.Addresses) != 0 {
			addresses = append(addresses, node.Addresses...)
			continue
		}
		if strict && node.IPv4 == "" && node.IPv6 == "" && len(node.ManualAddresses) == 0 {
			return nil, bizerrors.New(40000, fmt.Sprintf("节点 %q 暂无 IP，请补充手动 IP、注册节点或等待已注册的节点上报", node.Name), http.StatusBadRequest)
		}
		if node.IPv4 != "" {
			addresses = append(addresses, node.IPv4)
		}
		if node.IPv6 != "" {
			addresses = append(addresses, node.IPv6)
		}
	}
	slices.Sort(addresses)
	return slices.Compact(addresses), nil
}

func relayServiceName(id uint) string {
	return fmt.Sprintf("relay-tunnel-%d", id)
}

func (s *GostAllowlistService) prepareRelay(client *gost.Client, relay *gost.ServiceConfig, tunnel *model.GostTunnel) error {
	if len(tunnel.AllowlistNodeIDs) == 0 {
		return nil
	}
	current, err := client.GetConfig()
	if err != nil {
		return err
	}
	if _, err = s.ensureRelayAdmission(client, current.Admissions, relay.Name, tunnel.AllowlistNodeIDs); err != nil {
		return err
	}
	relay.Admission = gost.TunnelRelayAllowlistName(relay.Name)
	return nil
}

func (s *GostAllowlistService) ensureRelayAdmission(client *gost.Client, current []gost.AdmissionConfig, relayName string, nodeIDs []uint) (bool, error) {
	// Resolve current addresses on every sync so address changes are picked up without saving the selection again.
	addresses, err := s.resolveAddresses(context.Background(), nodeIDs, false)
	if err != nil {
		return false, err
	}
	return client.EnsureAdmission(gost.AdmissionConfig{
		Name:      gost.TunnelRelayAllowlistName(relayName),
		Whitelist: len(nodeIDs) > 0,
		Matchers:  addresses,
	}, current)
}

func (s *GostAllowlistService) syncRelays(client *gost.Client, current *gost.GostConfig, nodeID uint, tunnels []model.GostTunnel) (bool, error) {
	managed := make(map[string][]uint)
	for _, tunnel := range tunnels {
		if tunnel.ExitNodeID == nodeID && tunnel.ServiceID == relayServiceName(tunnel.ID) {
			managed[tunnel.ServiceID] = tunnel.AllowlistNodeIDs
		}
	}
	var changed bool
	var syncError error
	for _, relay := range current.Services {
		nodeIDs, managedRelay := managed[relay.Name]
		if !managedRelay || relay.Handler == nil || relay.Handler.Type != "relay" {
			continue
		}
		hasAdmission := relay.HasAdmission(gost.TunnelRelayAllowlistName(relay.Name))
		// Keep an existing per-tunnel binding when clearing the selection to avoid reloading the relay.
		enabled := len(nodeIDs) > 0 || hasAdmission
		if enabled {
			updated, err := s.ensureRelayAdmission(client, current.Admissions, relay.Name, nodeIDs)
			changed = changed || updated
			if err != nil {
				syncError = errors.Join(syncError, fmt.Errorf("%s: %w", relay.Name, err))
				continue
			}
		}
		if relay.HasAdmission(gost.LegacyRelayAllowlistName) || enabled != hasAdmission {
			updated, err := client.SetRelayAllowlist(relay.Name, enabled)
			changed = changed || updated
			if err != nil {
				syncError = errors.Join(syncError, fmt.Errorf("%s: %w", relay.Name, err))
			}
		}
	}
	return changed, syncError
}
