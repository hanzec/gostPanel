package service

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"yagp/internal/model"
	"yagp/pkg/geoip"
	"yagp/pkg/logger"

	"gorm.io/gorm"
)

func RunNodeRegions(ctx context.Context, db *gorm.DB) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := refreshNodeRegions(ctx, db, http.DefaultClient); err != nil && ctx.Err() == nil {
			logger.Warnf("更新节点地区失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func refreshNodeRegions(ctx context.Context, db *gorm.DB, client *http.Client) error {
	var nodes []model.GostNode
	query := db.WithContext(ctx).Model(&model.GostNode{})
	if err := query.Select("id", "ipv4", "ipv6").Where("region = '' AND (ipv4 <> '' OR ipv6 <> '')").Find(&nodes).Error; err != nil {
		return err
	}
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		code := nodeCountry(ctx, client, node.IPv4, node.IPv6)
		if code == "" {
			continue
		}
		// The address may have changed during the lookup.
		if err := db.WithContext(ctx).Model(&model.GostNode{}).Where("id = ? AND COALESCE(ipv4, '') = ? AND COALESCE(ipv6, '') = ?",
			node.ID, node.IPv4, node.IPv6).UpdateColumn("region", code).Error; err != nil {
			return err
		}
	}
	return nil
}

func nodeCountry(ctx context.Context, client *http.Client, addresses ...string) string {
	for _, address := range addresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
			continue
		}
		if code := geoip.Country(ctx, client, ip); code != "" {
			return code
		}
	}
	return ""
}
