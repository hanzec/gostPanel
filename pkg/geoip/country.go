package geoip

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Country looks up a public address. An invalid address looks up the caller's egress.
func Country(ctx context.Context, client *http.Client, ip netip.Addr) string {
	url := "https://get.geojs.io/v1/ip/country"
	if ip.IsValid() {
		url += "/" + ip.String()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 16))
	code := strings.TrimSpace(string(body))
	if err != nil || !isCountryCode(code) {
		return ""
	}
	return code
}

func isCountryCode(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}
