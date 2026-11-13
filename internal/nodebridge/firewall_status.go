package nodebridge

import (
	"context"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *controlServer) GetFirewallStatus(context.Context, *emptypb.Empty) (*nodegrpc.FirewallStatus, error) {
	if s.firewall == nil {
		return &nodegrpc.FirewallStatus{}, nil
	}
	state := s.firewall.Status()
	reply := &nodegrpc.FirewallStatus{
		Supported: state.Supported, Managed: state.Managed, Synchronized: state.Synchronized,
		LastError: state.LastError, Backend: state.Backend,
	}
	if state.LastSync != nil {
		reply.LastSync = timestamppb.New(*state.LastSync)
	}
	return reply, nil
}
