package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"ycgp/internal/config"
	"ycgp/pkg/response"

	"github.com/gin-gonic/gin"
)

func NewAuthentikAuth(cfg config.AuthentikConfig) (gin.HandlerFunc, error) {
	group := strings.TrimSpace(cfg.AdminGroup)
	if group == "" || strings.Contains(group, "|") {
		return nil, errors.New("authentik.admin_group 必须指定一个管理员组")
	}
	if len(cfg.TrustedProxies) == 0 {
		return nil, errors.New("authentik.trusted_proxies 必须指定可信反向代理")
	}

	proxies := make([]netip.Prefix, 0, len(cfg.TrustedProxies))
	for _, value := range cfg.TrustedProxies {
		prefix, err := netip.ParsePrefix(value)
		if addr, addrErr := netip.ParseAddr(value); addrErr == nil && addr.Zone() == "" {
			prefix, err = netip.PrefixFrom(addr, addr.BitLen()), nil
		}
		if err != nil || prefix.Bits() == 0 {
			return nil, fmt.Errorf("authentik.trusted_proxies 包含无效或不受限的地址 %q", value)
		}
		proxies = append(proxies, prefix)
	}

	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		req := c.Request
		usernames := req.Header.Values("X-Authentik-Username")
		if !trustedPeer(req.RemoteAddr, proxies) || len(usernames) != 1 || strings.TrimSpace(usernames[0]) == "" {
			response.Unauthorized(c, "请通过 Authentik 登录")
			c.Abort()
			return
		}
		groups := req.Header.Values("X-Authentik-Groups")
		if len(groups) != 1 || !slices.Contains(strings.Split(groups[0], "|"), group) {
			response.Forbidden(c, "需要 Authentik 管理员组权限")
			c.Abort()
			return
		}
		if !sameOriginRequest(req) {
			response.Forbidden(c, "拒绝跨站操作请求")
			c.Abort()
			return
		}
		c.Set("username", strings.TrimSpace(usernames[0]))
		c.Next()
	}, nil
}

func trustedPeer(remote string, proxies []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, prefix := range proxies {
		if prefix.Contains(addr) || prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

func sameOriginRequest(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	fetchSite := req.Header.Get("Sec-Fetch-Site")
	if fetchSite != "" && fetchSite != "same-origin" {
		return false
	}
	origins := req.Header.Values("Origin")
	if len(origins) == 0 {
		return fetchSite == "same-origin"
	}
	if len(origins) != 1 {
		return false
	}
	scheme := req.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if req.TLS != nil {
			scheme = "https"
		}
	}
	return (scheme == "http" || scheme == "https") && strings.EqualFold(origins[0], scheme+"://"+req.Host)
}
