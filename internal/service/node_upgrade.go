package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"yagp/internal/buildinfo"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"
	"yagp/pkg/upgrade"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const (
	nodeUpgradeFirstCheck = time.Minute
	nodeUpgradeInterval   = time.Hour
	nodeUpgradePoll       = 5 * time.Second
	nodeUpgradeWait       = 5 * time.Minute
	nodeUpgradeLookup     = 8 * time.Second
)

type NodeUpgradeView struct {
	model.NodeUpgradePolicy
	Available             bool   `json:"available"`
	Reason                string `json:"reason,omitempty"`
	GostVersion           string `json:"gost_version"`
	DaemonVersion         string `json:"daemon_version"`
	LatestGostVersion     string `json:"latest_gost_version"`
	LatestDaemonVersion   string `json:"latest_daemon_version"`
	GostUpdateAvailable   bool   `json:"gost_update_available"`
	DaemonUpdateAvailable bool   `json:"daemon_update_available"`
	Component             string `json:"component"`
	TargetVersion         string `json:"target_version"`
	State                 string `json:"state"`
	Error                 string `json:"error"`
}

type nodeUpgradeRelease struct {
	version   string
	err       error
	expiresAt time.Time
}

type NodeUpgradeService struct {
	db           *gorm.DB
	logService   *LogService
	releases     *upgrade.Client
	panelVersion string
	cacheMu      sync.Mutex
	cache        map[string]nodeUpgradeRelease
}

func NewNodeUpgradeService(db *gorm.DB) *NodeUpgradeService {
	return &NodeUpgradeService{
		db: db, logService: NewLogService(db), releases: upgrade.NewClient(), panelVersion: buildinfo.Version, cache: make(map[string]nodeUpgradeRelease),
	}
}

func (s *NodeUpgradeService) Get(ctx context.Context, id uint) (*NodeUpgradeView, error) {
	node, err := s.findNode(ctx, id)
	if err != nil {
		return nil, err
	}
	view := &NodeUpgradeView{NodeUpgradePolicy: node.UpgradePolicy}
	state, err := utils.GetGostClient(node).GetUpgradeStatus(ctx)
	if err != nil {
		switch status.Code(err) {
		case codes.Unimplemented:
			view.Reason = "当前节点守护程序版本不支持在线升级"
		case codes.FailedPrecondition:
			view.Reason = status.Convert(err).Message()
		default:
			view.Reason = "无法读取节点升级状态，请检查节点连接"
		}
		return view, nil
	}
	view.Available = state.GetSupported()
	view.GostVersion, view.DaemonVersion = state.GetGostVersion(), state.GetDaemonVersion()
	view.Component, view.TargetVersion = state.GetComponent(), state.GetTargetVersion()
	view.State, view.Error = state.GetState(), state.GetError()
	if !view.Available {
		view.Reason = "在线升级需要通过安装脚本部署的 Linux systemd 节点"
		return view, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	view.LatestGostVersion, err = s.cachedLatest(lookupCtx, "gost")
	view.LatestDaemonVersion = s.daemonTarget()
	if err != nil {
		view.Reason = "暂时无法查询 Gost 的最新稳定版本"
	}
	view.GostUpdateAvailable = upgrade.IsNewer("gost", view.GostVersion, view.LatestGostVersion)
	view.DaemonUpdateAvailable = nodeUpgradeAvailable(state, "daemon", view.LatestDaemonVersion)
	return view, nil
}

func (s *NodeUpgradeService) SetPolicy(ctx context.Context, id uint, policy model.NodeUpgradePolicy) error {
	node, err := s.findNode(ctx, id)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(node).Updates(map[string]any{
		"auto_upgrade_gost":   policy.AutoUpgradeGost,
		"auto_upgrade_daemon": policy.AutoUpgradeDaemon,
	}).Error
}

func (s *NodeUpgradeService) Upgrade(ctx context.Context, id uint, component string) (*nodegrpc.NodeUpgradeStatus, error) {
	if component != "gost" && component != "daemon" {
		return nil, status.Error(codes.InvalidArgument, "请选择要升级的组件")
	}
	node, err := s.findNode(ctx, id)
	if err != nil {
		return nil, err
	}
	client := utils.GetGostClient(node)
	state, err := client.GetUpgradeStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !state.GetSupported() {
		return nil, status.Error(codes.Unimplemented, "当前节点不支持在线升级")
	}
	if nodeUpgradeBusy(state) {
		return nil, status.Error(codes.FailedPrecondition, "此节点已有升级任务")
	}
	current := nodeComponentVersion(state, component)
	if current == "" {
		return nil, status.Error(codes.FailedPrecondition, "无法识别当前安装版本")
	}
	target := s.daemonTarget()
	if component == "gost" {
		lookupCtx, cancel := context.WithTimeout(ctx, nodeUpgradeLookup)
		target, err = s.releases.Latest(lookupCtx, component)
		cancel()
		if err != nil {
			logger.Warnf("查询 %s 最新版本失败: %v", component, err)
			return nil, bizerrors.New(50001, "无法查询最新稳定版本，请检查面板的网络连接", http.StatusBadGateway)
		}
	}
	if !nodeUpgradeAvailable(state, component, target) {
		return nil, status.Error(codes.FailedPrecondition, "没有可用的更高稳定版本")
	}
	return client.StartUpgrade(ctx, component, target)
}

func (s *NodeUpgradeService) Run(ctx context.Context) {
	timer := time.NewTimer(nodeUpgradeFirstCheck)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runAutomatic(ctx)
			timer.Reset(nodeUpgradeInterval)
		}
	}
}

func (s *NodeUpgradeService) runAutomatic(ctx context.Context) {
	var ids []uint
	if err := s.db.WithContext(ctx).Model(&model.GostNode{}).
		Where("status = ? AND (auto_upgrade_gost = ? OR auto_upgrade_daemon = ?)", model.NodeStatusOnline, true, true).
		Order("id").Pluck("id", &ids).Error; err != nil {
		logger.Warnf("查询启用自动升级的节点失败: %v", err)
		return
	}
	targets := make(map[string]string, 2)
	for _, id := range ids {
		for _, component := range []string{"gost", "daemon"} {
			if ctx.Err() != nil {
				return
			}
			if err := s.upgradeAutomatically(ctx, id, component, targets); err != nil {
				// Don't start another upgrade while the first one's worker may still be running.
				logger.Warnf("等待节点 %d 的 %s 自动升级结束失败: %v", id, component, err)
				return
			}
		}
	}
}

func (s *NodeUpgradeService) upgradeAutomatically(ctx context.Context, id uint, component string, targets map[string]string) error {
	// An administrator can change the policy while an earlier upgrade in this pass is running.
	node, err := s.findNode(ctx, id)
	if err != nil || node.Status != model.NodeStatusOnline {
		return nil
	}
	enabled := node.UpgradePolicy.AutoUpgradeDaemon
	if component == "gost" {
		enabled = node.UpgradePolicy.AutoUpgradeGost
	}
	if !enabled {
		return nil
	}
	client := utils.GetGostClient(node)
	state, err := client.GetUpgradeStatus(ctx)
	if err != nil || !state.GetSupported() || nodeUpgradeBusy(state) {
		return nil
	}
	target, checked := targets[component]
	if !checked {
		target, err = s.cachedLatest(ctx, component)
		if err != nil {
			logger.Warnf("自动升级查询 %s 最新版本失败: %v", component, err)
		}
		targets[component] = target
	}
	if !nodeUpgradeAvailable(state, component, target) {
		return nil
	}
	current := nodeComponentVersion(state, component)
	state, err = client.StartUpgrade(ctx, component, target)
	if err != nil {
		s.recordAutomaticUpgrade(node, component, current, target, "启动失败："+err.Error())
		logger.Warnf("启动节点 %d 的 %s 自动升级失败: %v", id, component, err)
		return nil
	}
	s.recordAutomaticUpgrade(node, component, current, target, "已启动")
	if nodeUpgradeBusy(state) {
		state, err = waitForNodeUpgrade(ctx, client, state.GetComponent(), state.GetTargetVersion())
		if err != nil {
			s.recordAutomaticUpgrade(node, component, current, target, "结果待确认："+err.Error())
			return err
		}
	}
	if state.GetState() == "succeeded" {
		s.recordAutomaticUpgrade(node, component, current, target, "成功")
	} else if state.GetState() == "failed" {
		s.recordAutomaticUpgrade(node, component, current, target, "失败："+state.GetError())
		logger.Warnf("节点 %d 的 %s 自动升级失败: %s", id, component, state.GetError())
	}
	return nil
}

func (s *NodeUpgradeService) recordAutomaticUpgrade(node *model.GostNode, component, current, target, result string) {
	name := "Gost"
	if component == "daemon" {
		name = "yagp-daemon"
	}
	s.logService.Record("系统", model.ActionAutoUpgrade, model.ResourceTypeNode, node.ID,
		fmt.Sprintf("节点 %s (#%d)：%s 自动升级%s（%s → %s）", node.Name, node.ID, name, result, current, target), "", "")
}

func (s *NodeUpgradeService) findNode(ctx context.Context, id uint) (*model.GostNode, error) {
	var node model.GostNode
	err := s.db.WithContext(ctx).First(&node, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, bizerrors.ErrNodeNotFound
	}
	return &node, err
}

func (s *NodeUpgradeService) cachedLatest(ctx context.Context, component string) (string, error) {
	if component == "daemon" {
		return s.daemonTarget(), nil
	}
	s.cacheMu.Lock()
	entry := s.cache[component]
	s.cacheMu.Unlock()
	if time.Now().Before(entry.expiresAt) {
		return entry.version, entry.err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, nodeUpgradeLookup)
	version, err := s.releases.Latest(lookupCtx, component)
	cancel()
	if errors.Is(err, context.Canceled) {
		return version, err
	}
	lifetime := 5 * time.Minute
	if err != nil {
		lifetime = time.Minute
	}
	s.cacheMu.Lock()
	s.cache[component] = nodeUpgradeRelease{version: version, err: err, expiresAt: time.Now().Add(lifetime)}
	s.cacheMu.Unlock()
	return version, err
}

func (s *NodeUpgradeService) daemonTarget() string {
	if upgrade.ValidateTag("daemon", s.panelVersion) {
		return s.panelVersion
	}
	return ""
}

func nodeComponentVersion(state *nodegrpc.NodeUpgradeStatus, component string) string {
	if component == "gost" {
		return state.GetGostVersion()
	}
	return state.GetDaemonVersion()
}

func nodeUpgradeAvailable(state *nodegrpc.NodeUpgradeStatus, component, target string) bool {
	current := nodeComponentVersion(state, component)
	return upgrade.IsNewer(component, current, target) ||
		(component == "daemon" && target != "" && current == target && state.GetRuntimeUpdateRequired())
}

func nodeUpgradeBusy(state *nodegrpc.NodeUpgradeStatus) bool {
	return state.GetState() == "queued" || state.GetState() == "running"
}

func waitForNodeUpgrade(ctx context.Context, client *gost.Client, component, target string) (*nodegrpc.NodeUpgradeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, nodeUpgradeWait)
	defer cancel()
	timer := time.NewTicker(nodeUpgradePoll)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("节点未确认任务结束: %w", ctx.Err())
		case <-timer.C:
			state, err := client.GetUpgradeStatus(ctx)
			if err == nil && state.GetComponent() == component && state.GetTargetVersion() == target &&
				(state.GetState() == "succeeded" || state.GetState() == "failed") {
				return state, nil
			}
		}
	}
}
