package handler

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type ReportHandler struct {
	nodegrpc.UnimplementedPanelServer
	db      *gorm.DB
	traffic *service.ObserverService
	nodes   *service.NodeService
}

func NewReportHandler(db *gorm.DB) *ReportHandler {
	return &ReportHandler{db: db, traffic: service.NewObserverService(db), nodes: service.NewNodeService(db)}
}

func (h *ReportHandler) RegisterNode(ctx context.Context, request *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
	id, err := nodegrpc.ParseRegistrationTokenID(request.GetToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired registration token")
	}
	if err := nodegrpc.ValidateNodeCertificate(request.GetNodeCertificatePem()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid node certificate")
	}
	node, err := h.nodes.Register(ctx, uint(id), request.GetToken(), request.GetNodeCertificatePem())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired registration token")
	}
	if err != nil {
		logger.Errorf("注册节点 %d 失败: %v", id, err)
		return nil, status.Error(codes.Internal, "could not register node")
	}
	return &nodegrpc.RegisterNodeReply{
		NodeId:      uint64(node.ID),
		Username:    node.Username,
		Password:    node.Password,
		ControlPort: uint32(node.Port),
	}, nil
}

func (h *ReportHandler) Report(ctx context.Context, request *nodegrpc.ReportRequest) (*observer.ObserveReply, error) {
	node, err := h.authenticateNode(ctx, request.GetNodeId())
	if err != nil {
		return nil, err
	}

	samples, err := trafficSamples(request.GetObservation())
	if err != nil {
		return nil, err
	}
	if err := h.traffic.RecordTraffic(ctx, node.ID, samples); err != nil {
		if errors.Is(err, service.ErrReportNodeMismatch) {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
		logger.Errorf("记录节点 %d 流量失败: %v", node.ID, err)
		return nil, status.Error(codes.Internal, "could not record traffic")
	}
	return &observer.ObserveReply{Ok: true}, nil
}

func (h *ReportHandler) authenticateNode(ctx context.Context, id uint64) (*model.GostNode, error) {
	var node model.GostNode
	err := h.db.WithContext(ctx).First(&node, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) ||
		(err == nil && (node.TLSCertificatePEM == "" || !nodegrpc.MatchCredentials(ctx, node.Username, node.Password))) {
		return nil, status.Error(codes.Unauthenticated, "invalid node credentials")
	}
	if err != nil {
		logger.Errorf("查询上报节点失败: %v", err)
		return nil, status.Error(codes.Internal, "could not look up node")
	}
	return &node, nil
}

func trafficSamples(observation *observer.ObserveRequest) ([]service.TrafficSample, error) {
	if observation == nil {
		return nil, status.Error(codes.InvalidArgument, "missing observation")
	}
	samples := make([]service.TrafficSample, 0, len(observation.Events))
	for _, event := range observation.Events {
		if event.GetKind() != "service" {
			continue
		}
		ruleID, managed := strings.CutPrefix(event.GetService(), "rule-")
		if !managed {
			continue
		}
		id, err := strconv.ParseUint(ruleID, 10, strconv.IntSize)
		if err != nil || id == 0 {
			continue
		}
		switch event.GetType() {
		case "status":
			if event.GetStatus().GetState() == "running" {
				samples = append(samples, service.TrafficSample{RuleID: uint(id), Reset: true})
			}
		case "stats":
			stats := event.GetStats()
			if stats == nil || stats.InputBytes > math.MaxInt64 || stats.OutputBytes > math.MaxInt64-stats.InputBytes || stats.TotalConns > math.MaxInt64 {
				return nil, status.Error(codes.InvalidArgument, "invalid traffic counters")
			}
			samples = append(samples, service.TrafficSample{
				RuleID: uint(id),
				ObserverCounters: model.ObserverCounters{
					InputBytes:  int64(stats.InputBytes),
					OutputBytes: int64(stats.OutputBytes),
					Connections: int64(stats.TotalConns),
				},
			})
		}
	}
	return samples, nil
}
