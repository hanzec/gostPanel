package nodebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	"yagp/pkg/gost"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

func diagnoseTunnelProbeFailure(ctx context.Context, err error, relay *gost.NodeConfig, gostFailure string) error {
	st := status.Convert(err)
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "yagp.tunnel_probe" {
			continue
		}
		evidence := make(map[string]string)
		if gostFailure != "" {
			evidence["gost_error"] = gostFailure
		}
		switch info.GetReason() {
		case "relay_write", "relay_read":
			if result := checkTunnelRelayTCP(ctx, relay); result != "" {
				evidence["relay_tcp"] = result
			}
		}
		return tunnelProbeStatus(st.Code(), st.Message(), info.GetReason(), evidence)
	}
	return err
}

func checkTunnelRelayTCP(ctx context.Context, relay *gost.NodeConfig) string {
	switch relay.Dialer.Type {
	case "tcp", "mtcp", "tls", "mtls", "ws", "wss", "mws", "mwss", "h2", "h2c", "grpc", "pht", "phts", "ohttp", "otls":
	case "udp", "kcp", "quic", "h3":
		return "not_applicable"
	default:
		return ""
	}
	if ctx.Err() != nil {
		return ""
	}
	// Use the saved relay address and reserve a separate, short budget after the data exchange.
	dialer := net.Dialer{Timeout: 600 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", relay.Addr)
	var dns *net.DNSError
	var networkError net.Error
	switch {
	case err == nil:
		_ = conn.Close()
		return "reachable"
	case ctx.Err() != nil:
		return ""
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "unreachable"
	case errors.As(err, &networkError) && networkError.Timeout():
		return "timeout"
	case errors.As(err, &dns):
		return "dns_error"
	default:
		return "unavailable"
	}
}

// Only report recognized failure categories from the private Gost process.
type tunnelProbeLog struct {
	line       [4096]byte
	used       int
	discard    bool
	failure    string
	recognized chan struct{}
}

func (l *tunnelProbeLog) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 && l.failure == "" {
		line, rest, complete := bytes.Cut(data, []byte{'\n'})
		if len(line) > len(l.line)-l.used {
			l.discard = true
		}
		if !l.discard {
			l.used += copy(l.line[l.used:], line)
		}
		if complete {
			if !l.discard {
				l.failure = tunnelProbeLogFailure(l.line[:l.used])
			}
			if l.failure != "" && l.recognized != nil {
				close(l.recognized)
			}
			l.used, l.discard = 0, false
		}
		data = rest
	}
	return size, nil
}

func tunnelProbeLogFailure(line []byte) string {
	var log struct {
		Message string `json:"msg"`
		Level   string `json:"level"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(line, &log); err != nil {
		return ""
	}
	if log.Level == "fatal" && strings.HasPrefix(log.Message, "unregistered dialer: ") {
		return "dialer_unsupported"
	}
	if log.Service != "yagp-tunnel-probe" {
		return ""
	}
	// Gost logs the Relay protocol status when the local forwarder pipes the connection.
	switch log.Message {
	case "pipe: 2 Unauthorized":
		return "relay_authentication"
	case "pipe: 3 Forbidden":
		return "relay_target_forbidden"
	case "pipe: 6 Host Unreachable", "pipe: 7 Network Unreachable":
		return "relay_target_unreachable"
	case "websocket: bad handshake":
		return "relay_handshake"
	}
	if log.Level == "error" &&
		(strings.HasPrefix(log.Message, "tls: ") || strings.HasPrefix(log.Message, "remote error: tls: ")) {
		return "relay_tls"
	}
	return ""
}
