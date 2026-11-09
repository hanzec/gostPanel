package nodebridge

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"yagp/pkg/nodegrpc"
)

func reportPublicIPChanges(ctx context.Context, nodeID uint64, panel nodegrpc.V1Client,
	detect func(context.Context) (string, string, []string), ticks <-chan time.Time,
) {
	var lastIPv4, lastIPv6 string
	var lastAddresses []string
	for ctx.Err() == nil {
		ipv4, ipv6, addresses := detect(ctx)
		if (ipv4 != "" && ipv4 != lastIPv4) || (ipv6 != "" && ipv6 != lastIPv6) ||
			(len(addresses) != 0 && !slices.Equal(addresses, lastAddresses)) {
			rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := panel.UpdateNodeIPs(rpcCtx, &nodegrpc.NodeIPsRequest{NodeId: nodeID, Ipv4: ipv4, Ipv6: ipv6, Addresses: addresses})
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("report public IP changes: %v", err)
			}
			lastIPv4 = acknowledgedIP(lastIPv4, ipv4, err == nil)
			lastIPv6 = acknowledgedIP(lastIPv6, ipv6, err == nil)
			if err == nil {
				if len(addresses) != 0 {
					lastIPv4, lastIPv6 = ipv4, ipv6
				}
				lastAddresses = addresses
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

func detectPublicAddresses(ctx context.Context) (string, string, []string) {
	ipv4, ipv6 := DetectPublicIPs(ctx)
	interfaces, _ := net.InterfaceAddrs()
	return publicAddresses(ipv4, ipv6, interfaces)
}

func publicAddresses(ipv4, ipv6 string, interfaces []net.Addr) (string, string, []string) {
	var addresses []string
	for _, address := range []string{ipv4, ipv6} {
		if address != "" {
			addresses = append(addresses, address)
		}
	}
	for _, address := range interfaces {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		ip := prefix.Addr().Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
			continue
		}
		addresses = append(addresses, ip.String())
	}
	slices.Sort(addresses)
	addresses = slices.Compact(addresses)
	for _, address := range addresses {
		if strings.Contains(address, ":") {
			if ipv6 == "" {
				ipv6 = address
			}
		} else if ipv4 == "" {
			ipv4 = address
		}
	}
	return ipv4, ipv6, addresses
}

func acknowledgedIP(previous, sent string, acknowledged bool) string {
	if sent == "" {
		return previous
	}
	// A failed response may follow a committed update, so reconcile on the next probe.
	if !acknowledged {
		return ""
	}
	return sent
}

// DetectPublicIPs returns the node's public IPv4 and IPv6, leaving failed probes empty.
func DetectPublicIPs(ctx context.Context) (string, string) {
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
