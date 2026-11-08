package nodegrpc

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"

	"google.golang.org/grpc"
)

const PanelProxyPath = "/api/v1/grpc"

type PanelAddress struct {
	target  string
	proxied bool
}

func ParsePanelAddress(address string) (PanelAddress, error) {
	target, proxied := strings.CutSuffix(address, PanelProxyPath)
	host, port, err := net.SplitHostPort(target)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || strings.ContainsAny(host, "/ \\?#@\t\r\n") || portErr != nil || portNumber == 0 {
		return PanelAddress{}, errors.New("panel address must be HOST:PORT or HOST:PORT" + PanelProxyPath)
	}
	return PanelAddress{target: target, proxied: proxied}, nil
}

func (a PanelAddress) Proxied() bool { return a.proxied }

func (a PanelAddress) NewClientConn(options ...grpc.DialOption) (*grpc.ClientConn, error) {
	if a.proxied {
		// Traefik removes this prefix before forwarding the standard gRPC method path.
		options = append(options, grpc.WithChainUnaryInterceptor(
			func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				return invoke(ctx, PanelProxyPath+method, req, reply, cc, opts...)
			},
		))
	}
	return grpc.NewClient(a.target, options...)
}
