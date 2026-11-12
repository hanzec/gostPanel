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
	_, err := s.resolveAddresses(ctx, nodeIDs, true, false)
	return nodeIDs, err
}

func (s *GostAllowlistService) resolveAddresses(ctx context.Context, nodeIDs []uint, strict, includeLocal bool) ([]string, error) {
	addresses := []string{}
	if len(nodeIDs) == 0 {
		return addresses, nil
	}
	var nodes []model.GostNode
	if err := s.db.WithContext(ctx).Select("id", "name", "ipv4", "ipv6", "addresses", "manual_addresses", "local_addresses").Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
		return nil, err
	}
	if strict && len(nodes) != len(nodeIDs) {
		return nil, bizerrors.New(40000, "白名单包含不存在的节点", http.StatusBadRequest)
	}
	for _, node := range nodes {
		addresses = append(addresses, node.ManualAddresses...)
		if includeLocal {
			addresses = append(addresses, node.LocalAddresses...)
		}
		if len(node.Addresses) != 0 {
			addresses = append(addresses, node.Addresses...)
			continue
		}
		if strict && node.IPv4 == "" && node.IPv6 == "" && len(node.ManualAddresses) == 0 {
			return nil, bizerrors.New(40000, fmt.Sprintf("节点 %q 暂无来源 IP，请注册节点或等待节点上报", node.Name), http.StatusBadRequest)
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
	if !tunnel.AutoAllowlist && len(tunnel.AllowlistNodeIDs) == 0 {
		return nil
	}
	desired, err := s.relayAdmission(tunnel)
	if err != nil {
		return err
	}
	if desired == nil {
		// A rebuilt relay may have a different entry; wait for its addresses instead of reusing an old admission.
		return nil
	}
	current, err := client.GetConfig()
	if err != nil {
		return err
	}
	if _, err = client.EnsureAdmission(*desired, current.Admissions); err != nil {
		return err
	}
	relay.Admission = gost.TunnelRelayAllowlistName(relay.Name)
	return nil
}

func (s *GostAllowlistService) relayAdmission(tunnel *model.GostTunnel) (*gost.AdmissionConfig, error) {
	nodeIDs := tunnel.AllowlistNodeIDs
	if tunnel.AutoAllowlist {
		nodeIDs = []uint{tunnel.EntryNodeID}
	}
	// Resolve current addresses on every sync so address changes are picked up without saving the selection again.
	addresses, err := s.resolveAddresses(context.Background(), nodeIDs, false, tunnel.AutoAllowlist)
	if err != nil {
		return nil, err
	}
	if tunnel.AutoAllowlist && len(addresses) == 0 {
		return nil, nil
	}
	return &gost.AdmissionConfig{
		Name:      gost.TunnelRelayAllowlistName(relayServiceName(tunnel.ID)),
		Whitelist: len(nodeIDs) > 0,
		Matchers:  addresses,
	}, nil
}

func (s *GostAllowlistService) syncRelays(client *gost.Client, current *gost.GostConfig, nodeID uint, tunnels []model.GostTunnel) (bool, error) {
	managed := make(map[string]model.GostTunnel)
	for _, tunnel := range tunnels {
		if tunnel.ExitNodeID == nodeID && tunnel.ServiceID == relayServiceName(tunnel.ID) {
			managed[tunnel.ServiceID] = tunnel
		}
	}
	var changed bool
	var syncError error
	for _, relay := range current.Services {
		tunnel, managedRelay := managed[relay.Name]
		if !managedRelay || relay.Handler == nil || relay.Handler.Type != "relay" {
			continue
		}
		hasAdmission := relay.HasAdmission(gost.TunnelRelayAllowlistName(relay.Name))
		// Keep an existing per-tunnel binding when clearing the selection to avoid reloading the relay.
		enabled := tunnel.AutoAllowlist || len(tunnel.AllowlistNodeIDs) > 0 || hasAdmission
		if enabled {
			desired, err := s.relayAdmission(&tunnel)
			if err != nil {
				syncError = errors.Join(syncError, fmt.Errorf("%s: %w", relay.Name, err))
				continue
			}
			if desired == nil {
				// Keep the applied list during a temporary loss of the running entry's address snapshot.
				continue
			}
			updated, err := client.EnsureAdmission(*desired, current.Admissions)
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
