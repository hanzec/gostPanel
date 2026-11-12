package handler

import (
	"context"
	"net/netip"
	"time"

	"yagp/internal/model"
	"yagp/pkg/geoip"
	"yagp/pkg/logger"
)

func (h *NodeHandler) setNodeRegions(ctx context.Context, nodes ...*model.GostNode) {
	if h.geoIP == nil {
		return
	}
	var fallbackCtx context.Context
	for _, node := range nodes {
		for _, address := range []string{node.IPv4, node.IPv6} {
			ip, err := netip.ParseAddr(address)
			if err != nil {
				continue
			}
			var code string
			if fallbackCtx == nil {
				code, err = h.geoIP.Country(ctx, ip)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					logger.Warnf("GeoIP 数据库查询失败，回退到旧版 GeoJS 在线查询: %v", err)
					// One time budget covers online lookups for the whole response.
					var cancel context.CancelFunc
					fallbackCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
					defer cancel()
				}
			}
			if fallbackCtx != nil {
				if fallbackCtx.Err() != nil {
					return
				}
				ip = ip.Unmap()
				if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
					continue
				}
				code = geoip.Country(fallbackCtx, h.geoIPClient, ip)
			}
			if code != "" {
				node.Region = code
				break
			}
		}
	}
}
