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
	"gorm.io/gorm/clause"
)

const gostAllowlistID = 1

type GostAllowlistService struct {
	db *gorm.DB
}

func NewGostAllowlistService(db *gorm.DB) *GostAllowlistService {
	return &GostAllowlistService{db: db}
}

func (s *GostAllowlistService) Get(ctx context.Context) (model.GostAllowlist, error) {
	config := model.GostAllowlist{NodeIDs: []uint{}, Addresses: []string{}}
	if err := s.db.WithContext(ctx).Find(&config, gostAllowlistID).Error; err != nil {
		return model.GostAllowlist{}, err
	}
	// Nodes can report new IPs at runtime; resolve their current addresses on each sync.
	addresses, err := s.resolveAddresses(ctx, config.NodeIDs, false)
	if err != nil {
		return model.GostAllowlist{}, err
	}
	config.Addresses = addresses
	return config, nil
}

func (s *GostAllowlistService) Update(ctx context.Context, nodeIDs []uint) (model.GostAllowlist, error) {
	nodeIDs = append([]uint{}, nodeIDs...)
	slices.Sort(nodeIDs)
	config := model.GostAllowlist{ID: gostAllowlistID, NodeIDs: slices.Compact(nodeIDs)}
	addresses, err := s.resolveAddresses(ctx, config.NodeIDs, true)
	if err != nil {
		return config, err
	}
	config.Addresses = addresses
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&config).Error
	return config, err
}

func (s *GostAllowlistService) resolveAddresses(ctx context.Context, nodeIDs []uint, strict bool) ([]string, error) {
	addresses := []string{}
	if len(nodeIDs) == 0 {
		return addresses, nil
	}
	var nodes []model.GostNode
	if err := s.db.WithContext(ctx).Select("id", "name", "ipv4", "ipv6").Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
		return nil, err
	}
	if strict && len(nodes) != len(nodeIDs) {
		return nil, bizerrors.New(40000, "白名单包含不存在的节点", http.StatusBadRequest)
	}
	for _, node := range nodes {
		if strict && node.IPv4 == "" && node.IPv6 == "" {
			return nil, bizerrors.New(40000, fmt.Sprintf("节点 %q 暂无 IP，请先注册节点或等待已注册的节点上报", node.Name), http.StatusBadRequest)
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

func relayAdmission(config model.GostAllowlist) gost.AdmissionConfig {
	return gost.AdmissionConfig{
		Name:      gost.RelayAllowlistName,
		Whitelist: len(config.NodeIDs) > 0,
		Matchers:  config.Addresses,
	}
}

func relayServiceName(id uint) string {
	return fmt.Sprintf("relay-tunnel-%d", id)
}

func (s *GostAllowlistService) prepareRelay(client *gost.Client, relay *gost.ServiceConfig) error {
	config, err := s.Get(context.Background())
	if err != nil || config.ID == 0 {
		return err
	}
	current, err := client.GetConfig()
	if err != nil {
		return err
	}
	if _, err = client.EnsureAdmission(relayAdmission(config), current.Admissions); err != nil {
		return err
	}
	relay.Admission = gost.RelayAllowlistName
	return nil
}

func syncGostAllowlist(client *gost.Client, current *gost.GostConfig, nodeID uint, tunnels []model.GostTunnel, config model.GostAllowlist) error {
	managed := make(map[string]bool)
	for _, tunnel := range tunnels {
		if tunnel.ExitNodeID == nodeID && tunnel.ServiceID == relayServiceName(tunnel.ID) {
			managed[tunnel.ServiceID] = true
		}
	}
	var relays []gost.ServiceConfig
	for _, svc := range current.Services {
		if managed[svc.Name] && svc.Handler != nil && svc.Handler.Type == "relay" &&
			(len(config.NodeIDs) > 0 || svc.HasRelayAllowlist()) {
			relays = append(relays, svc)
		}
	}
	if len(relays) == 0 {
		return nil
	}
	changed, err := client.EnsureAdmission(relayAdmission(config), current.Admissions)
	if err != nil {
		return err
	}
	var syncError error
	for _, relay := range relays {
		if !relay.HasRelayAllowlist() {
			updated, err := client.AttachRelayAllowlist(relay.Name)
			changed = changed || updated
			if err != nil {
				syncError = errors.Join(syncError, fmt.Errorf("%s: %w", relay.Name, err))
			}
		}
	}
	if changed {
		syncError = errors.Join(syncError, client.SaveConfig())
	}
	return syncError
}
