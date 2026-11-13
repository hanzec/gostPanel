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

var sharedNATRange = netip.MustParsePrefix("100.64.0.0/10")

type nodeIPSnapshot struct {
	ipv4, ipv6     string
	public, local  []string
	mptcpSupported *bool
}

func reportIPChanges(ctx context.Context, nodeID uint64, panel nodegrpc.V1Client,
	detect func(context.Context) nodeIPSnapshot, ticks <-chan time.Time,
) {
	var lastIPv4, lastIPv6 string
	var lastAddresses []string
	var lastLocal []string
	var lastMPTCP *bool
	for ctx.Err() == nil {
		snapshot := detect(ctx)
		publicChanged := (snapshot.ipv4 != "" && snapshot.ipv4 != lastIPv4) || (snapshot.ipv6 != "" && snapshot.ipv6 != lastIPv6) ||
			(len(snapshot.public) != 0 && !slices.Equal(snapshot.public, lastAddresses))
		localChanged := snapshot.local != nil && (lastLocal == nil || !slices.Equal(snapshot.local, lastLocal))
		mptcpChanged := snapshot.mptcpSupported != nil && (lastMPTCP == nil || *snapshot.mptcpSupported != *lastMPTCP)
		if publicChanged || localChanged || mptcpChanged {
			request := &nodegrpc.NodeIPsRequest{NodeId: nodeID, Ipv4: snapshot.ipv4, Ipv6: snapshot.ipv6, Addresses: snapshot.public}
			if localChanged || (mptcpChanged && snapshot.local != nil) {
				request.LocalIpSnapshot = &nodegrpc.LocalIPSnapshot{Addresses: snapshot.local}
			}
			request.MptcpSupported = snapshot.mptcpSupported
			rpcCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := panel.UpdateNodeIPs(rpcCtx, request)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("report node IP changes: %v", err)
			}
			lastIPv4 = acknowledgedIP(lastIPv4, snapshot.ipv4, err == nil)
			lastIPv6 = acknowledgedIP(lastIPv6, snapshot.ipv6, err == nil)
			if err == nil {
				if len(snapshot.public) != 0 {
					lastIPv4, lastIPv6 = snapshot.ipv4, snapshot.ipv6
				}
				lastAddresses = snapshot.public
			}
			if localChanged {
				lastLocal = nil
				if err == nil {
					lastLocal = snapshot.local
				}
			}
			if mptcpChanged {
				lastMPTCP = nil
				if err == nil {
					lastMPTCP = snapshot.mptcpSupported
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

func detectNodeIPs(ctx context.Context) nodeIPSnapshot {
	ipv4, ipv6 := DetectPublicIPs(ctx)
	interfaces, err := localNodeInterfaceAddresses(ctx)
	snapshot := collectNodeIPs(ipv4, ipv6, interfaces)
	mptcpSupported := detectMultipathTCP()
	snapshot.mptcpSupported = &mptcpSupported
	if err != nil {
		snapshot.local = nil
	}
	return snapshot
}

func collectNodeIPs(ipv4, ipv6 string, interfaces []net.Addr) nodeIPSnapshot {
	var addresses []string
	local := make([]string, 0, len(interfaces))
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
		if !ip.IsGlobalUnicast() || ip.Zone() != "" {
			continue
		}
		local = append(local, ip.String())
		if !ip.IsPrivate() && !sharedNATRange.Contains(ip) {
			addresses = append(addresses, ip.String())
		}
	}
	slices.Sort(local)
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
	return nodeIPSnapshot{ipv4: ipv4, ipv6: ipv6, public: addresses, local: slices.Compact(local)}
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

	const fallback = "https://icanhazip.com/"
	ipv6 := make(chan string, 1)
	go func() { ipv6 <- firstPublicIP(ctx, "tcp6", "https://api6.ipify.org", fallback) }()
	ipv4 := firstPublicIP(ctx, "tcp4", "https://api.ipify.org", fallback)
	return ipv4, <-ipv6
}

func firstPublicIP(ctx context.Context, network string, endpoints ...string) string {
	for _, endpoint := range endpoints {
		if ctx.Err() != nil {
			break
		}
		// Reserve time for the fallback even if the first service never responds.
		attempt, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		ip := probePublicIP(attempt, network, endpoint)
		cancel()
		if ip != "" {
			return ip
		}
	}
	return ""
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
	if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || sharedNATRange.Contains(ip) || ip.Zone() != "" || ip.Is4In6() || ip.Is4() != (network == "tcp4") {
		return ""
	}
	return ip.String()
}
