package nodebridge

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"syscall"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *controlServer) CheckListener(ctx context.Context, req *nodegrpc.ListenerCheckRequest) (*nodegrpc.ListenerCheckReply, error) {
	protocol, ip, port := req.GetProtocol(), req.GetIp(), req.GetPort()
	if (protocol != "tcp" && protocol != "udp") || port == 0 || port > 65535 {
		return nil, status.Error(codes.InvalidArgument, "监听检查需要 TCP 或 UDP 协议及有效端口")
	}
	if ip != "" {
		addr, err := netip.ParseAddr(ip)
		if err != nil || addr.Zone() != "" {
			return nil, status.Error(codes.InvalidArgument, "监听 IP 必须是有效的 IPv4 或 IPv6 地址")
		}
	}
	address := net.JoinHostPort(ip, strconv.FormatUint(uint64(port), 10))
	var config net.ListenConfig
	var socket io.Closer
	var err error
	if protocol == "tcp" {
		socket, err = config.Listen(ctx, protocol, address)
	} else {
		socket, err = config.ListenPacket(ctx, protocol, address)
	}
	if err != nil {
		return &nodegrpc.ListenerCheckReply{Reason: listenerBindFailure(err)}, nil
	}
	_ = socket.Close()
	return &nodegrpc.ListenerCheckReply{Available: true}, nil
}

func listenerBindFailure(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "occupied"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "address_unavailable"
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "permission_denied"
	default:
		return "bind_failed"
	}
}
