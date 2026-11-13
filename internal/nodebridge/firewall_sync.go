package nodebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"
)

const firewallSyncInterval = 15 * time.Second

func (s *controlServer) maintainFirewall(ctx context.Context) {
	ticker := time.NewTicker(firewallSyncInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.syncFirewall(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.firewallChanged:
		}
	}
}

func (s *controlServer) syncFirewall(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ports, err := s.firewallPorts(ctx)
	if err != nil {
		s.firewall.recordSyncError(err)
		return
	}
	_ = s.firewall.Reconcile(ctx, ports)
}

func (s *controlServer) firewallPorts(ctx context.Context) (FirewallPorts, error) {
	// Gost loads its saved listeners on boot and reports their current state over loopback.
	reply, err := s.Execute(ctx, &nodegrpc.CommandRequest{Method: http.MethodGet, Path: "/config"})
	if err != nil {
		return FirewallPorts{}, fmt.Errorf("read Gost listeners: %w", err)
	}
	if reply.GetStatusCode() != http.StatusOK {
		return FirewallPorts{}, fmt.Errorf("read Gost listeners: HTTP %d", reply.GetStatusCode())
	}
	var config struct {
		Services json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(reply.GetBody(), &config); err != nil {
		return FirewallPorts{}, fmt.Errorf("decode Gost configuration: %w", err)
	}
	if config.Services == nil {
		return FirewallPorts{}, fmt.Errorf("Gost configuration is missing services")
	}
	var services []gost.ServiceConfig
	if err := json.Unmarshal(config.Services, &services); err != nil {
		return FirewallPorts{}, fmt.Errorf("decode Gost services: %w", err)
	}
	return managedFirewallPorts(services), nil
}

func managedFirewallPorts(services []gost.ServiceConfig) FirewallPorts {
	var ports FirewallPorts
	for _, service := range services {
		if !service.IsManagedForwarder() || service.Listener == nil || service.Status == nil ||
			(service.Status.State != "running" && service.Status.State != "ready") {
			continue
		}
		host, rawPort, err := net.SplitHostPort(service.Addr)
		if err != nil {
			continue
		}
		port, err := strconv.ParseUint(rawPort, 10, 16)
		if err != nil || port == 0 {
			continue
		}
		var addr netip.Addr
		if host != "" {
			addr, err = netip.ParseAddr(host)
			if err != nil || addr.Zone() != "" {
				continue
			}
			addr = addr.Unmap()
			if addr.IsLoopback() || addr.IsUnspecified() {
				continue
			}
		}
		switch firewallTransport(service.Listener) {
		case "tcp":
			addFirewallEndpoint(&ports.TCP, &ports.TCPAddresses, addr, uint16(port))
		case "udp":
			addFirewallEndpoint(&ports.UDP, &ports.UDPAddresses, addr, uint16(port))
		}
	}
	return ports
}

func addFirewallEndpoint(ports *[]uint16, addresses *[]FirewallEndpoint, addr netip.Addr, port uint16) {
	if addr.IsValid() {
		*addresses = append(*addresses, FirewallEndpoint{Address: addr, Port: port})
	} else {
		*ports = append(*ports, port)
	}
}

func firewallTransport(listener *gost.ListenerConfig) string {
	switch listener.Type {
	case "tcp", "mtcp", "tls", "mtls", "ws", "mws", "wss", "mwss", "h2", "h2c", "grpc", "pht", "phts", "ohttp", "otls":
		return "tcp"
	case "udp", "quic", "h3":
		return "udp"
	case "kcp":
		for _, key := range []string{"kcp.tcp", "tcp"} {
			if value, ok := listener.Metadata[key]; ok {
				if enabled, _ := strconv.ParseBool(strings.TrimSpace(fmt.Sprint(value))); enabled {
					return "tcp"
				}
				break
			}
		}
		return "udp"
	default:
		return ""
	}
}
