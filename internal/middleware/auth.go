package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"yagp/internal/config"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

const (
	authentikPageKey    = "authentik_page"
	authentikFailureKey = "authentik_failure"
)

// MarkAuthentikPage selects an HTML response for authentication failures on a page route.
func MarkAuthentikPage(c *gin.Context) {
	c.Set(authentikPageKey, true)
}

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
		if !trustedPeer(req.RemoteAddr, proxies) {
			denyAuthentik(c, untrustedProxy)
			return
		}
		usernames := req.Header.Values("X-Authentik-Username")
		var username string
		if len(usernames) == 1 {
			username = strings.TrimSpace(usernames[0])
		}
		if username == "" {
			denyAuthentik(c, invalidIdentity)
			return
		}
		groups := req.Header.Values("X-Authentik-Groups")
		if len(groups) != 1 || !slices.Contains(strings.Split(groups[0], "|"), group) {
			denyAuthentik(c, adminGroupRequired)
			return
		}
		if !sameOriginRequest(req) {
			denyAuthentik(c, crossOriginRequest)
			return
		}
		c.Set("username", username)
		c.Next()
	}, nil
}

func denyAuthentik(c *gin.Context, failure authentikRejection) {
	c.Set(authentikFailureKey, failure.Reason)
	c.Abort()
	if c.GetBool(authentikPageKey) {
		renderAuthentikErrorPage(c, failure)
		return
	}
	if failure.Status == http.StatusUnauthorized {
		response.Unauthorized(c, failure.Message)
		return
	}
	response.Forbidden(c, failure.Message)
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
