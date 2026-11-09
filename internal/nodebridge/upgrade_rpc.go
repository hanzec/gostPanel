package nodebridge

import (
	"context"

	"yagp/pkg/nodegrpc"
	"yagp/pkg/upgrade"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *controlServer) GetUpgradeStatus(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.NodeUpgradeStatus, error) {
	current, err := ReadUpgradeStatus(ctx, s.config, s.config.ConfigFile)
	if err != nil {
		return nil, status.Error(codes.Internal, "无法读取节点升级状态")
	}
	if current.State == "" {
		current.State = "idle"
	}
	return &nodegrpc.NodeUpgradeStatus{
		Supported: current.Supported, GostVersion: current.GostVersion, DaemonVersion: current.DaemonVersion,
		RuntimeUpdateRequired: current.RuntimeUpdateRequired,
		Component:             current.Component, TargetVersion: current.Tag, State: current.State, Error: current.Message,
	}, nil
}

func (s *controlServer) StartUpgrade(ctx context.Context, request *nodegrpc.NodeUpgradeRequest) (*nodegrpc.NodeUpgradeStatus, error) {
	component, tag := request.GetComponent(), request.GetVersion()
	if !upgrade.ValidateTag(component, tag) {
		return nil, status.Error(codes.InvalidArgument, "无效的升级组件或版本")
	}
	if !supportsUpgrade(s.config.ConfigFile) {
		return nil, status.Error(codes.Unimplemented, "在线升级需要 Linux systemd 节点")
	}
	if err := StartUpgrade(ctx, s.config, s.config.ConfigFile, component, tag); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "无法启动节点升级任务: %v", err)
	}
	return &nodegrpc.NodeUpgradeStatus{Supported: true, Component: component, TargetVersion: tag, State: "queued"}, nil
}
