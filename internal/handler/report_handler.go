package handler

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

const connectAuthTimeout = 3 * time.Second

type ReportHandler struct {
	nodegrpc.UnimplementedV1Server
	db           *gorm.DB
	traffic      *service.ObserverService
	nodes        *service.NodeService
	nodeTokens   *service.NodeCredentialService
	behindProxy  bool
	reportLimits reportRateLimits
}

func NewReportHandler(db *gorm.DB, behindProxy bool) *ReportHandler {
	return &ReportHandler{
		db: db, traffic: service.NewObserverService(db), nodes: service.NewNodeService(db, ""), behindProxy: behindProxy,
	}
}

func (h *ReportHandler) newNodeSession(stream nodegrpc.Stream, reports *streamReports) *nodegrpc.Session {
	methods := nodegrpc.NewDispatcher(reports,
		nodegrpc.V1_Report_FullMethodName, nodegrpc.V1_UpdateNodeIPs_FullMethodName, nodegrpc.V1_GetNodeConfig_FullMethodName)
	return nodegrpc.NewSession(stream, func(ctx context.Context, method string, payload []byte) ([]byte, error) {
		switch method {
		case nodegrpc.V1_Report_FullMethodName, nodegrpc.V1_UpdateNodeIPs_FullMethodName:
			if !h.reportLimits.allow(uint(reports.id), method) {
				return nil, status.Error(codes.ResourceExhausted, "node report rate limit exceeded")
			}
		}
		return methods(ctx, method, payload)
	})
}

type streamReports struct {
	*ReportHandler
	id   uint64
	node *model.GostNode
}

func (h *streamReports) nodeForRequest(id uint64) (*model.GostNode, error) {
	if id != h.id {
		return nil, status.Error(codes.Unauthenticated, "invalid node identity")
	}
	return h.node, nil
}

func (h *streamReports) UpdateNodeIPs(ctx context.Context, request *nodegrpc.NodeIPsRequest) (*emptypb.Empty, error) {
	node, err := h.nodeForRequest(request.GetNodeId())
	if err != nil {
		return nil, err
	}
	ipv4, ipv6, err := reportedIPs(request.GetIpv4(), request.GetIpv6())
	if err != nil {
		return nil, err
	}
	if ipv4 == "" && ipv6 == "" && request.GetLocalIpSnapshot() == nil && request.MptcpSupported == nil {
		return nil, status.Error(codes.InvalidArgument, "missing node IP addresses")
	}
	addresses, err := reportedAddresses(request.GetAddresses(), ipv4, ipv6)
	if err != nil {
		return nil, err
	}
	localAddresses, err := reportedLocalAddresses(request.GetLocalIpSnapshot())
	if err != nil {
		return nil, err
	}
	if err := h.nodes.UpdateIPs(ctx, node, ipv4, ipv6, addresses, localAddresses, request.GetMptcpSupported()); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.Unauthenticated, "invalid node credentials")
		}
		logger.Errorf("更新节点 %d IP 失败: %v", node.ID, err)
		return nil, status.Error(codes.Internal, "could not update node IP addresses")
	}
	return &emptypb.Empty{}, nil
}

func reportedLocalAddresses(snapshot *nodegrpc.LocalIPSnapshot) ([]string, error) {
	if snapshot == nil {
		return nil, nil
	}
	addresses := make([]string, 0, len(snapshot.Addresses))
	for _, address := range snapshot.Addresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.Zone() != "" || !ip.Unmap().IsGlobalUnicast() {
			return nil, status.Error(codes.InvalidArgument, "invalid node local address")
		}
		addresses = append(addresses, ip.Unmap().String())
	}
	slices.Sort(addresses)
	return slices.Compact(addresses), nil
}

func reportedAddresses(addresses []string, ipv4, ipv6 string) ([]string, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(addresses)+2)
	for _, address := range addresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || !isNodeIP(ip.Unmap()) {
			return nil, status.Error(codes.InvalidArgument, "invalid node public address")
		}
		result = append(result, ip.Unmap().String())
	}
	for _, address := range []string{ipv4, ipv6} {
		if address != "" {
			result = append(result, address)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func (h *ReportHandler) registrationIPs(ctx context.Context, request *nodegrpc.RegisterNodeRequest) (string, string, error) {
	ipv4, ipv6, err := reportedIPs(request.GetIpv4(), request.GetIpv6())
	if err != nil || ipv4 != "" || ipv6 != "" || h.behindProxy {
		return ipv4, ipv6, err
	}
	var ip netip.Addr
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		endpoint, _ := netip.ParseAddrPort(p.Addr.String())
		ip = endpoint.Addr()
	}
	ip = ip.Unmap()
	if !isNodeIP(ip) {
		return "", "", nil
	}
	if ip.Is4() {
		return ip.String(), "", nil
	}
	return "", ip.String(), nil
}

func reportedIPs(v4, v6 string) (string, string, error) {
	ipv4, valid := parseReportedIP(v4, 32)
	if !valid {
		return "", "", status.Error(codes.InvalidArgument, "invalid node IPv4")
	}
	ipv6, valid := parseReportedIP(v6, 128)
	if !valid {
		return "", "", status.Error(codes.InvalidArgument, "invalid node IPv6")
	}
	return ipv4, ipv6, nil
}

func parseReportedIP(value string, bits int) (string, bool) {
	if value == "" {
		return "", true
	}
	ip, err := netip.ParseAddr(value)
	ip = ip.Unmap()
	if err != nil || ip.BitLen() != bits || !isNodeIP(ip) {
		return "", false
	}
	return ip.String(), true
}

func isNodeIP(ip netip.Addr) bool {
	return ip.Zone() == "" && (ip.IsGlobalUnicast() || ip.IsLoopback())
}

func (h *streamReports) Report(ctx context.Context, request *nodegrpc.ReportRequest) (*observer.ObserveReply, error) {
	node, err := h.nodeForRequest(request.GetNodeId())
	if err != nil {
		return nil, err
	}

	samples, err := trafficSamples(request.GetObservation())
	if err != nil {
		return nil, err
	}
	if err := h.traffic.RecordTraffic(ctx, node.ID, samples); err != nil {
		if errors.Is(err, repository.ErrTrafficCounterOverflow) {
			return nil, status.Error(codes.InvalidArgument, "traffic totals exceed supported range")
		}
		if errors.Is(err, service.ErrReportNodeMismatch) {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
		logger.Errorf("记录节点 %d 流量失败: %v", node.ID, err)
		return nil, status.Error(codes.Internal, "could not record traffic")
	}
	return &observer.ObserveReply{Ok: true}, nil
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
