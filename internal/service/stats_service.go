package service

import (
	"strconv"

	"yagp/internal/buildinfo"
	"yagp/internal/model"
	"yagp/internal/repository"

	"gorm.io/gorm"
)

// StatsService 统计服务
type StatsService struct {
	nodeRepo   *repository.NodeRepository
	ruleRepo   *repository.RuleRepository
	tunnelRepo *repository.TunnelRepository
	database   string
}

// NewStatsService 创建统计服务
func NewStatsService(db *gorm.DB) *StatsService {
	return &StatsService{
		nodeRepo:   repository.NewNodeRepository(db),
		ruleRepo:   repository.NewRuleRepository(db),
		tunnelRepo: repository.NewTunnelRepository(db),
		database:   db.Dialector.Name(),
	}
}

// DashboardStats 仪表盘统计
type DashboardStats struct {
	Nodes    NodeStats        `json:"nodes"`
	Rules    RuleStats        `json:"rules"`
	Tunnels  TunnelStats      `json:"tunnels"`
	Traffic  DashboardTraffic `json:"traffic"`
	Version  string           `json:"version"`
	Database string           `json:"database"`
}

type DashboardTraffic struct {
	repository.TrafficTotals
	// Keep the existing per-tunnel projection available to API clients.
	Tunnels []repository.TunnelExitTraffic `json:"tunnels"`
	Exits   []DashboardExitTraffic         `json:"exits"`
}

type DashboardExitTraffic struct {
	Key string `json:"key"`
	repository.EndpointExitTraffic
}

// NodeStats 节点统计
type NodeStats struct {
	Total   int64 `json:"total"`
	Online  int64 `json:"online"`
	Offline int64 `json:"offline"`
}

// RuleStats 规则统计
type RuleStats struct {
	Total       int64 `json:"total"`
	Running     int64 `json:"running"`
	Stopped     int64 `json:"stopped"`
	ForwardType int64 `json:"forward_type"` // 端口转发类型数量
	TunnelType  int64 `json:"tunnel_type"`  // 隧道转发类型数量
}

// TunnelStats 隧道统计
type TunnelStats struct {
	Total   int64 `json:"total"`
	Running int64 `json:"running"`
	Stopped int64 `json:"stopped"`
}

// GetDashboardStats 获取仪表盘统计
func (s *StatsService) GetDashboardStats() (*DashboardStats, error) {
	stats := &DashboardStats{}

	// 节点统计
	nodeTotal, err := s.nodeRepo.CountAll()
	if err != nil {
		return nil, err
	}
	nodeOnline, err := s.nodeRepo.CountByStatus("online")
	if err != nil {
		return nil, err
	}
	stats.Nodes = NodeStats{
		Total:   nodeTotal,
		Online:  nodeOnline,
		Offline: nodeTotal - nodeOnline,
	}

	// 规则统计
	ruleTotal, err := s.ruleRepo.CountAll()
	if err != nil {
		return nil, err
	}
	ruleRunning, err := s.ruleRepo.CountByStatus(model.RuleStatusRunning)
	if err != nil {
		return nil, err
	}
	forwardType, err := s.ruleRepo.CountByType(model.RuleTypeForward)
	if err != nil {
		return nil, err
	}
	tunnelType, err := s.ruleRepo.CountByType(model.RuleTypeTunnel)
	if err != nil {
		return nil, err
	}
	stats.Rules = RuleStats{
		Total:       ruleTotal,
		Running:     ruleRunning,
		Stopped:     ruleTotal - ruleRunning,
		ForwardType: forwardType,
		TunnelType:  tunnelType,
	}

	// 隧道统计
	tunnelTotal, err := s.tunnelRepo.CountAll()
	if err != nil {
		return nil, err
	}
	tunnelRunning, err := s.tunnelRepo.CountByStatus(model.TunnelStatusRunning)
	if err != nil {
		return nil, err
	}
	stats.Tunnels = TunnelStats{
		Total:   tunnelTotal,
		Running: tunnelRunning,
		Stopped: tunnelTotal - tunnelRunning,
	}
	stats.Traffic.Tunnels, err = s.tunnelRepo.ExitTraffic()
	if err != nil {
		return nil, err
	}
	endpoints, err := s.tunnelRepo.EndpointExitTraffic()
	if err != nil {
		return nil, err
	}
	stats.Traffic.collectExits(endpoints)

	stats.Version = buildinfo.Version
	stats.Database = s.database

	return stats, nil
}

func (t *DashboardTraffic) collectExits(endpoints []repository.EndpointExitTraffic) {
	t.Exits = make([]DashboardExitTraffic, 0, len(endpoints))
	for _, endpoint := range endpoints {
		t.Exits = append(t.Exits, DashboardExitTraffic{
			Key: "exit:" + strconv.FormatUint(uint64(endpoint.ID), 10), EndpointExitTraffic: endpoint,
		})
	}
	for _, tunnel := range t.Tunnels {
		if tunnel.TypedExit {
			continue
		}
		t.Exits = append(t.Exits, DashboardExitTraffic{
			Key: "tunnel:" + strconv.FormatUint(uint64(tunnel.ID), 10),
			EndpointExitTraffic: repository.EndpointExitTraffic{
				ID: tunnel.ID, Name: tunnel.Name, ExitNodeID: tunnel.ExitNodeID,
				ExitNodeName: tunnel.ExitNodeName, ExitNodeStatus: tunnel.ExitNodeStatus,
				TrafficTotals: tunnel.TrafficTotals, Running: tunnel.Status == model.TunnelStatusRunning, Measured: tunnel.Measured,
			},
		})
	}
	for _, exit := range t.Exits {
		t.InputBytes += exit.InputBytes
		t.OutputBytes += exit.OutputBytes
	}
}
