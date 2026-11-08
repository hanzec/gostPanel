package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

func detectPublicIPs(ctx context.Context) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ipv6 := make(chan string, 1)
	go func() { ipv6 <- probePublicIP(ctx, "tcp6", "https://api6.ipify.org") }()
	ipv4 := probePublicIP(ctx, "tcp4", "https://api.ipify.org")
	return ipv4, <-ipv6
}

func probePublicIP(ctx context.Context, network, endpoint string) string {
	dialer := net.Dialer{}
	transport := &http.Transport{
		// Use the node's own route so an HTTP proxy cannot supply its own address.
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
	body, err := io.ReadAll(io.LimitReader(res.Body, 65))
	if err != nil || len(body) > 64 {
		return ""
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" || ip.Is4In6() || ip.Is4() != (network == "tcp4") {
		return ""
	}
	return ip.String()
}
