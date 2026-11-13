package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	gostReleaseLookup     = 8 * time.Second
)

type NodeUpgradeView struct {
	model.NodeUpgradePolicy
	Available             bool   `json:"available"`
	Reason                string `json:"reason,omitempty"`
	GostVersion           string `json:"gost_version"`
	DaemonVersion         string `json:"daemon_version"`
	LatestDaemonVersion   string `json:"latest_daemon_version"`
	DaemonUpdateAvailable bool   `json:"daemon_update_available"`
	Component             string `json:"component"`
	TargetVersion         string `json:"target_version"`
	State                 string `json:"state"`
	Error                 string `json:"error"`

	// Older clients still decode these fields. Release lookups only occur on manual Gost upgrades.
	LatestGostVersion   string `json:"latest_gost_version"`
	GostUpdateAvailable bool   `json:"gost_update_available"`
}

type NodeUpgradeService struct {
	db           *gorm.DB
	logService   *LogService
	panelVersion string
}

func NewNodeUpgradeService(db *gorm.DB) *NodeUpgradeService {
	return &NodeUpgradeService{
		db: db, logService: NewLogService(db), panelVersion: buildinfo.Version,
	}
}

func (s *NodeUpgradeService) Get(ctx context.Context, id uint) (*NodeUpgradeView, error) {
	node, err := s.findNode(ctx, id)
	if err != nil {
		return nil, err
	}
	view := &NodeUpgradeView{NodeUpgradePolicy: model.NodeUpgradePolicy{AutoUpgradeDaemon: node.UpgradePolicy.AutoUpgradeDaemon}}
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
	view.LatestDaemonVersion = s.daemonTarget()
	view.DaemonUpdateAvailable = nodeUpgradeAvailable(state, "daemon", view.LatestDaemonVersion)
	return view, nil
}

func (s *NodeUpgradeService) SetPolicy(ctx context.Context, id uint, autoUpgradeDaemon bool) error {
	node, err := s.findNode(ctx, id)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(node).Updates(map[string]any{
		"auto_upgrade_gost":   false,
		"auto_upgrade_daemon": autoUpgradeDaemon,
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
	if nodeComponentVersion(state, component) == "" {
		return nil, status.Error(codes.FailedPrecondition, "无法识别当前安装版本")
	}
	target := s.daemonTarget()
	if component == "gost" {
		lookupCtx, cancel := context.WithTimeout(ctx, gostReleaseLookup)
		target, err = upgrade.NewClient().Latest(lookupCtx, component)
		cancel()
		if err != nil {
			logger.Warnf("查询 Gost 最新版本失败: %v", err)
			return nil, bizerrors.New(50001, "无法查询 Gost 最新稳定版本，请检查面板的网络连接", http.StatusBadGateway)
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
		Where("status = ? AND auto_upgrade_daemon = ?", model.NodeStatusOnline, true).
		Order("id").Pluck("id", &ids).Error; err != nil {
		logger.Warnf("查询启用自动升级的节点失败: %v", err)
		return
	}
	target := s.daemonTarget()
	if target == "" {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := s.upgradeAutomatically(ctx, id, target); err != nil {
			// Don't start another upgrade while the first one's worker may still be running.
			logger.Warnf("等待节点 %d 的 yagp-daemon 自动升级结束失败: %v", id, err)
			return
		}
	}
}

func (s *NodeUpgradeService) upgradeAutomatically(ctx context.Context, id uint, target string) error {
	// An administrator can change the policy while an earlier upgrade in this pass is running.
	node, err := s.findNode(ctx, id)
	if err != nil || node.Status != model.NodeStatusOnline || !node.UpgradePolicy.AutoUpgradeDaemon {
		return nil
	}
	client := utils.GetGostClient(node)
	state, err := client.GetUpgradeStatus(ctx)
	if err != nil || !state.GetSupported() || nodeUpgradeBusy(state) || !nodeUpgradeAvailable(state, "daemon", target) {
		return nil
	}
	current := state.GetDaemonVersion()
	state, err = s.runAutomaticUpgrade(ctx, node, client, current, target, false)
	if err != nil || current == target || state.GetState() != "succeeded" ||
		state.GetDaemonVersion() != target || !state.GetRuntimeUpdateRequired() {
		return err
	}
	// An older worker can install the new binary without migrating the runtime layout.
	node, err = s.findNode(ctx, id)
	if err != nil || node.Status != model.NodeStatusOnline || !node.UpgradePolicy.AutoUpgradeDaemon {
		return nil
	}
	state, err = client.GetUpgradeStatus(ctx)
	if err != nil || !runtimeMigrationPending(state, target) {
		return nil
	}
	_, err = s.runAutomaticUpgrade(ctx, node, client, target, target, true)
	return err
}

func (s *NodeUpgradeService) runAutomaticUpgrade(ctx context.Context, node *model.GostNode, client *gost.Client, current, target string, followup bool) (*nodegrpc.NodeUpgradeStatus, error) {
	state, err := client.StartUpgrade(ctx, "daemon", target)
	if followup && upgradeWorkerUnitBusy(err, node.ID) {
		// A worker can publish its result just before systemd releases its transient unit name.
		timer := time.NewTimer(nodeUpgradePoll)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		node, statusErr := s.findNode(ctx, node.ID)
		if statusErr != nil || node.Status != model.NodeStatusOnline || !node.UpgradePolicy.AutoUpgradeDaemon {
			return nil, nil
		}
		latest, statusErr := client.GetUpgradeStatus(ctx)
		if statusErr != nil || !runtimeMigrationPending(latest, target) {
			return latest, nil
		}
		state, err = client.StartUpgrade(ctx, "daemon", target)
	}
	if err != nil {
		s.recordAutomaticUpgrade(node, current, target, "启动失败："+err.Error())
		logger.Warnf("启动节点 %d 的 yagp-daemon 自动升级失败: %v", node.ID, err)
		return nil, nil
	}
	s.recordAutomaticUpgrade(node, current, target, "已启动")
	if nodeUpgradeBusy(state) {
		state, err = waitForNodeUpgrade(ctx, client, state.GetComponent(), state.GetTargetVersion(), followup)
		if err != nil {
			s.recordAutomaticUpgrade(node, current, target, "结果待确认："+err.Error())
			return nil, err
		}
	}
	if state.GetState() == "succeeded" {
		s.recordAutomaticUpgrade(node, current, target, "成功")
	} else if state.GetState() == "failed" {
		s.recordAutomaticUpgrade(node, current, target, "失败："+state.GetError())
		logger.Warnf("节点 %d 的 yagp-daemon 自动升级失败: %s", node.ID, state.GetError())
	}
	return state, nil
}

func runtimeMigrationPending(state *nodegrpc.NodeUpgradeStatus, target string) bool {
	return state.GetSupported() && state.GetDaemonVersion() == target && state.GetRuntimeUpdateRequired() && !nodeUpgradeBusy(state)
}

func upgradeWorkerUnitBusy(err error, nodeID uint) bool {
	if status.Code(err) != codes.FailedPrecondition {
		return false
	}
	message := status.Convert(err).Message()
	return strings.Contains(message, fmt.Sprintf("yagp-upgrade-%d.service", nodeID)) &&
		(strings.Contains(message, "already loaded") || strings.Contains(message, "already exists"))
}

func (s *NodeUpgradeService) recordAutomaticUpgrade(node *model.GostNode, current, target, result string) {
	s.logService.Record("系统", model.ActionAutoUpgrade, model.ResourceTypeNode, node.ID,
		fmt.Sprintf("节点 %s (#%d)：yagp-daemon 自动升级%s（%s → %s）", node.Name, node.ID, result, current, target), "", "")
}

func (s *NodeUpgradeService) findNode(ctx context.Context, id uint) (*model.GostNode, error) {
	var node model.GostNode
	err := s.db.WithContext(ctx).First(&node, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, bizerrors.ErrNodeNotFound
	}
	return &node, err
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

func waitForNodeUpgrade(ctx context.Context, client *gost.Client, component, target string, requireMigratedRuntime bool) (*nodegrpc.NodeUpgradeStatus, error) {
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
				(state.GetState() == "failed" || (state.GetState() == "succeeded" && (!requireMigratedRuntime || !state.GetRuntimeUpdateRequired()))) {
				return state, nil
			}
		}
	}
}
