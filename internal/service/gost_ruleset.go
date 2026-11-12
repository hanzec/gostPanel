package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/logger"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	gostRulesetID   = 1
	maxRulesetBytes = 2 << 20
	rulesetRefresh  = time.Hour
	rulesetSync     = 30 * time.Second
)

type GostRulesetService struct {
	db     *gorm.DB
	client *http.Client
}

func NewGostRulesetService(db *gorm.DB) *GostRulesetService {
	return &GostRulesetService{db: db.Session(&gorm.Session{Logger: gormlogger.Discard}), client: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("规则集链接的重定向无效")
			}
			req.Header.Del("Referer")
			return nil
		},
	}}
}

func (s *GostRulesetService) Get(ctx context.Context) (*model.GostRuleset, error) {
	var ruleset model.GostRuleset
	err := s.db.WithContext(ctx).First(&ruleset, gostRulesetID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &model.GostRuleset{}, nil
	}
	return &ruleset, err
}

func (s *GostRulesetService) Update(ctx context.Context, source string, ids []uint) (*model.GostRuleset, error) {
	source = strings.TrimSpace(source)
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if source == "" && len(ids) > 0 {
		return nil, errors.New("选择目标节点前请填写规则集链接")
	}
	if len(ids) > 0 {
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.GostNode{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != int64(len(ids)) {
			return nil, errors.New("部分目标节点不存在，请刷新节点列表")
		}
	}
	ruleset := &model.GostRuleset{ID: gostRulesetID, URL: source, NodeIDs: ids}
	if source != "" {
		matchers, err := s.download(ctx, source)
		if err != nil {
			return nil, err
		}
		ruleset.Matchers = matchers
	}
	if err := s.db.WithContext(ctx).Save(ruleset).Error; err != nil {
		return nil, errors.New("保存 Gost 规则集失败")
	}
	return ruleset, nil
}

func (s *GostRulesetService) download(ctx context.Context, source string) ([]string, error) {
	address, err := url.ParseRequestURI(source)
	if err != nil || len(source) > 4096 || address.Scheme != "https" || address.Hostname() == "" || address.User != nil {
		return nil, errors.New("规则集链接必须是 HTTPS 地址")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, errors.New("规则集链接无效")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, errors.New("无法下载规则集，请检查链接和面板的网络连接")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("规则集下载失败（HTTP %d）", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRulesetBytes+1))
	if err != nil {
		return nil, errors.New("无法读取规则集内容")
	}
	if len(data) > maxRulesetBytes {
		return nil, errors.New("规则集大小超过 2 MiB")
	}
	return ParseClashRules(data)
}

func (s *GostRulesetService) Run(ctx context.Context) {
	refresh := time.NewTicker(rulesetRefresh)
	defer refresh.Stop()
	sync := time.NewTicker(rulesetSync)
	defer sync.Stop()
	unsaved := make(map[uint]bool)
	s.sync(ctx, unsaved)
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			s.refresh(ctx)
			s.sync(ctx, unsaved)
		case <-sync.C:
			s.sync(ctx, unsaved)
		}
	}
}

func (s *GostRulesetService) refresh(ctx context.Context) {
	cfg, err := s.Get(ctx)
	if err != nil || cfg.URL == "" {
		return
	}
	matchers, err := s.download(ctx, cfg.URL)
	if err != nil {
		logger.Warnf("刷新 Gost 规则集失败: %v", err)
		return
	}
	// A refresh may finish after the administrator has saved another source.
	now := time.Now()
	cfg.Matchers, cfg.UpdatedAt = matchers, &now
	if err := s.db.WithContext(ctx).Model(cfg).Where("url = ?", cfg.URL).Select("Matchers", "UpdatedAt").Updates(cfg).Error; err != nil {
		logger.Warn("无法保存刷新的 Gost 规则集")
	}
}

func (s *GostRulesetService) sync(ctx context.Context, unsaved map[uint]bool) {
	cfg, err := s.Get(ctx)
	if err != nil || cfg.URL == "" {
		return
	}
	var entryIDs []uint
	if err := s.db.WithContext(ctx).Table("tunnels").
		Distinct("tunnels.entry_node_id").
		Joins("JOIN rules ON (rules.tunnel_id = tunnels.id OR rules.id = tunnels.entry_rule_id)").
		Where("((tunnels.entry_rule_id IS NOT NULL AND COALESCE(tunnels.strategy, '') <> '' AND tunnels.use_clash_ruleset = ?) OR ((tunnels.entry_rule_id IS NULL OR COALESCE(tunnels.strategy, '') = '') AND rules.use_clash_ruleset = ?))", true, true).
		Where("rules.status = ? AND rules.deleted_at IS NULL AND tunnels.deleted_at IS NULL", model.RuleStatusRunning).
		Pluck("tunnels.entry_node_id", &entryIDs).Error; err != nil {
		logger.Warnf("读取使用 Clash 分流的入口失败: %v", err)
	}
	nodeIDs := slices.Concat(cfg.NodeIDs, entryIDs)
	if len(nodeIDs) == 0 {
		return
	}
	var nodes []model.GostNode
	if err := s.db.WithContext(ctx).Where("id IN ? AND status = ?", nodeIDs, model.NodeStatusOnline).Find(&nodes).Error; err != nil {
		return
	}
	for i := range nodes {
		if ctx.Err() != nil {
			return
		}
		client := utils.GetGostClient(&nodes[i])
		changed, err := client.SetTransparentBypass(cfg.Matchers)
		if err != nil {
			logger.Warnf("同步节点 %d 的 Gost 规则集失败: %v", nodes[i].ID, err)
			continue
		}
		if changed || unsaved[nodes[i].ID] {
			if err := client.SaveConfig(); err != nil {
				unsaved[nodes[i].ID] = true
				logger.Warnf("保存节点 %d 的 Gost 规则集失败: %v", nodes[i].ID, err)
			} else {
				delete(unsaved, nodes[i].ID)
			}
		}
	}
}
