package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"

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
	return config, nil
}

func (s *GostAllowlistService) Update(ctx context.Context, nodeIDs []uint) (model.GostAllowlist, error) {
	nodeIDs = append([]uint{}, nodeIDs...)
	slices.Sort(nodeIDs)
	config := model.GostAllowlist{ID: gostAllowlistID, NodeIDs: slices.Compact(nodeIDs), Addresses: []string{}}
	if len(config.NodeIDs) > 0 {
		var nodes []model.GostNode
		if err := s.db.WithContext(ctx).Select("id", "name", "address").Where("id IN ?", config.NodeIDs).Find(&nodes).Error; err != nil {
			return config, err
		}
		if len(nodes) != len(config.NodeIDs) {
			return config, bizerrors.New(40000, "白名单包含不存在的节点", http.StatusBadRequest)
		}
		for _, node := range nodes {
			ip, err := netip.ParseAddr(strings.Trim(node.Address, "[]"))
			if err != nil || ip.Zone() != "" {
				return config, bizerrors.New(40000, fmt.Sprintf("节点 %q 的地址须为 IPv4 或 IPv6", node.Name), http.StatusBadRequest)
			}
			config.Addresses = append(config.Addresses, ip.Unmap().String())
		}
		slices.Sort(config.Addresses)
		config.Addresses = slices.Compact(config.Addresses)
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&config).Error
	return config, err
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
