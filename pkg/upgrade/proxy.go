package upgrade

import (
	"context"
	"log"
	"net/netip"
	"os"

	"yagp/pkg/geoip"
)

const ghFast = "https://ghfast.top/"

// AssetURLs returns the preferred archive URL and, for automatic selection, its direct fallback.
func (c *Client) AssetURLs(ctx context.Context, url string) []string {
	if prefix, explicit := os.LookupEnv("GH_PROXY"); explicit {
		return []string{prefix + url}
	}
	switch code := geoip.Country(ctx, c.http, netip.Addr{}); code {
	case "CN", "HK", "MO":
		log.Printf("GitHub download region %s: using gh-fast", code)
		return []string{ghFast + url, url}
	default:
		return []string{url}
	}
}
