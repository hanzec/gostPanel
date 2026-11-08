package nodegrpc

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"path"
	"regexp"
	"strings"

	"google.golang.org/grpc"
)

const (
	PanelRPCPath = "/api/grpc"
	PanelPort    = "443"
)

type PanelHost struct{ host string }

var panelDNSLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func ParsePanelHost(host string) (PanelHost, error) {
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" {
		return PanelHost{host: address.String()}, nil
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 {
		return PanelHost{}, errors.New("panel host name exceeds 253 bytes")
	}
	for _, label := range strings.Split(host, ".") {
		if !panelDNSLabel.MatchString(label) {
			return PanelHost{}, errors.New("panel host must be a bare DNS name or IP address")
		}
	}
	return PanelHost{host: strings.ToLower(host)}, nil
}

func (h PanelHost) Host() string { return h.host }

func (h PanelHost) NewClientConn(options ...grpc.DialOption) (*grpc.ClientConn, error) {
	options = append(options, grpc.WithChainUnaryInterceptor(
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoke(ctx, PanelRPCPath+"/"+path.Base(method), req, reply, cc, opts...)
		},
	), grpc.WithChainStreamInterceptor(
		func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, stream grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return stream(ctx, desc, cc, PanelRPCPath+"/"+path.Base(method), opts...)
		},
	))
	return grpc.NewClient(net.JoinHostPort(h.host, PanelPort), options...)
}

// RegisterPanelServer exposes the panel service at the same path for direct and proxied connections.
func RegisterPanelServer(server grpc.ServiceRegistrar, service V1Server) {
	desc := V1_ServiceDesc
	desc.ServiceName = strings.TrimPrefix(PanelRPCPath, "/")
	server.RegisterService(&desc, service)
}
