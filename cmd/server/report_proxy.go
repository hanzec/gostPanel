package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func reportProxyOptions(proxyAddress string) ([]grpc.ServerOption, error) {
	if proxyAddress == "" {
		// The report handler only uses addresses supplied by the node in this mode.
		return nil, nil
	}
	proxyIP, err := netip.ParseAddr(proxyAddress)
	proxyIP = proxyIP.Unmap()
	if err != nil || proxyIP.Zone() != "" || !(proxyIP.IsGlobalUnicast() || proxyIP.IsLoopback()) {
		return nil, fmt.Errorf("report.proxy_ip 必须是代理的固定 IP 地址")
	}
	trusted := func(ctx context.Context) bool {
		if p, ok := peer.FromContext(ctx); ok {
			if addr, ok := p.Addr.(*net.TCPAddr); ok {
				return addr.AddrPort().Addr().Unmap() == proxyIP
			}
		}
		return false
	}
	return []grpc.ServerOption{
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			if !trusted(ctx) {
				return nil, status.Error(codes.PermissionDenied, "untrusted report proxy")
			}
			return next(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			if !trusted(stream.Context()) {
				return status.Error(codes.PermissionDenied, "untrusted report proxy")
			}
			return next(srv, stream)
		}),
	}, nil
}
