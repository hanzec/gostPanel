package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
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
	if source == "" {
		if err := s.requireUnreferenced(ctx, gostRulesetID); err != nil {
			return nil, err
		}
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
	ruleset, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	ruleset.ID, ruleset.URL, ruleset.NodeIDs = gostRulesetID, source, ids
	ruleset.Matchers = nil
	if ruleset.Name == "" {
		ruleset.Name = "默认规则集"
	}
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

func (s *GostRulesetService) List(ctx context.Context) ([]model.GostRuleset, error) {
	rulesets := []model.GostRuleset{}
	err := s.db.WithContext(ctx).Where("id <> ? OR url <> ?", gostRulesetID, "").Order("id").Find(&rulesets).Error
	return rulesets, err
}

func (s *GostRulesetService) Create(ctx context.Context, name, source string) (*model.GostRuleset, error) {
	ruleset, err := s.prepareSubscription(ctx, name, source)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reserve the legacy singleton ID before allocating a named subscription.
		reserved := model.GostRuleset{ID: gostRulesetID, Name: "默认规则集"}
		if err := tx.FirstOrCreate(&reserved, model.GostRuleset{ID: gostRulesetID}).Error; err != nil {
			return err
		}
		return tx.Create(ruleset).Error
	})
	return ruleset, err
}

func (s *GostRulesetService) UpdateByID(ctx context.Context, id uint, name, source string) (*model.GostRuleset, error) {
	var current model.GostRuleset
	if err := s.db.WithContext(ctx).First(&current, id).Error; err != nil {
		return nil, err
	}
	ruleset, err := s.prepareSubscription(ctx, name, source)
	if err != nil {
		return nil, err
	}
	current.Name, current.URL, current.Matchers = ruleset.Name, ruleset.URL, ruleset.Matchers
	if err := s.db.WithContext(ctx).Save(&current).Error; err != nil {
		return nil, err
	}
	return &current, nil
}

func (s *GostRulesetService) Delete(ctx context.Context, id uint) error {
	if id == gostRulesetID {
		return errors.New("默认规则集不能删除，请在原有规则集设置中清空链接")
	}
	var ruleset model.GostRuleset
	if err := s.db.WithContext(ctx).First(&ruleset, id).Error; err != nil {
		return err
	}
	if err := s.requireUnreferenced(ctx, id); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Delete(&ruleset).Error
}

func (s *GostRulesetService) prepareSubscription(ctx context.Context, name, source string) (*model.GostRuleset, error) {
	name, source = strings.TrimSpace(name), strings.TrimSpace(source)
	if name == "" || utf8.RuneCountInString(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return nil, errors.New("规则集名称须为 1 到 100 个字符")
	}
	matchers, err := s.download(ctx, source)
	if err != nil {
		return nil, err
	}
	return &model.GostRuleset{Name: name, URL: source, Matchers: matchers}, nil
}

func (s *GostRulesetService) requireUnreferenced(ctx context.Context, id uint) error {
	var rules []model.GostRule
	if err := s.db.WithContext(ctx).Select("id", "ruleset_ids").Find(&rules).Error; err != nil {
		return err
	}
	for _, rule := range rules {
		if slices.Contains(rule.RulesetIDs, id) {
			return fmt.Errorf("规则集正在被端点 %d 使用，请先移除引用", rule.ID)
		}
	}
	var tunnels []model.GostTunnel
	if err := s.db.WithContext(ctx).Select("id", "ruleset_ids").Find(&tunnels).Error; err != nil {
		return err
	}
	for _, tunnel := range tunnels {
		if slices.Contains(tunnel.RulesetIDs, id) {
			return fmt.Errorf("规则集正在被隧道 %d 使用，请先移除引用", tunnel.ID)
		}
	}
	return nil
}

// NormalizeSelection validates form values while preserving explicit Gost matcher syntax.
func (s *GostRulesetService) NormalizeSelection(ctx context.Context, ids []uint, domains []string) ([]uint, []string, error) {
	if len(ids) > 500 || len(domains) > 1000 {
		return nil, nil, bizerrors.InvalidRulesetSelection("最多选择 500 个规则集和填写 1000 条手动规则")
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > 0 && ids[0] == 0 {
		return nil, nil, bizerrors.InvalidRulesetSelection("规则集 ID 无效")
	}
	if _, err := s.selectedRulesets(ctx, ids); err != nil {
		return nil, nil, err
	}
	normalized := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		value, err := normalizeManualDomainRule(domain)
		if err != nil {
			return nil, nil, bizerrors.InvalidRulesetSelection(fmt.Sprintf("手填规则 %q 无效：%v", domain, err))
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			normalized = append(normalized, value)
		}
	}
	return ids, normalized, nil
}

// SelectedMatchers combines saved selections; manual rules without a port default to 443.
func (s *GostRulesetService) SelectedMatchers(ctx context.Context, ids []uint, domains []string) ([]string, error) {
	rulesets, err := s.selectedRulesets(ctx, ids)
	if err != nil {
		return nil, err
	}
	matchers := []string{}
	seen := make(map[string]struct{})
	for _, ruleset := range rulesets {
		for _, matcher := range ruleset.Matchers {
			if _, exists := seen[matcher]; !exists {
				seen[matcher] = struct{}{}
				matchers = append(matchers, matcher)
			}
		}
	}
	for _, domain := range domains {
		matcher := domain
		// Bare domains retain the panel's original suffix semantics; explicit patterns are native Gost rules.
		if !strings.HasPrefix(domain, ".") && !strings.ContainsAny(domain, "*?:") {
			matcher = "." + domain
		}
		if !strings.Contains(domain, ":") {
			matcher += ":443"
		}
		if _, exists := seen[matcher]; !exists {
			seen[matcher] = struct{}{}
			matchers = append(matchers, matcher)
		}
	}
	return matchers, nil
}

func (s *GostRulesetService) selectedRulesets(ctx context.Context, ids []uint) ([]model.GostRuleset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rulesets []model.GostRuleset
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Order("id").Find(&rulesets).Error; err != nil {
		return nil, err
	}
	if len(rulesets) != len(ids) {
		for _, id := range ids {
			if !slices.ContainsFunc(rulesets, func(ruleset model.GostRuleset) bool { return ruleset.ID == id }) {
				return nil, bizerrors.InvalidRulesetSelection(fmt.Sprintf("规则集 %d 不存在，请刷新后重新选择", id))
			}
		}
	}
	for _, ruleset := range rulesets {
		if ruleset.URL == "" || len(ruleset.Matchers) == 0 {
			return nil, bizerrors.InvalidRulesetSelection(fmt.Sprintf("规则集 %d 尚无有效规则，请先配置订阅", ruleset.ID))
		}
	}
	return rulesets, nil
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
	var rulesets []model.GostRuleset
	if err := s.db.WithContext(ctx).Where("url <> ?", "").Find(&rulesets).Error; err != nil {
		return
	}
	for i := range rulesets {
		if ctx.Err() != nil {
			return
		}
		cfg := &rulesets[i]
		matchers, err := s.download(ctx, cfg.URL)
		if err != nil {
			logger.Warnf("刷新 Gost 规则集 %d 失败: %v", cfg.ID, err)
			continue
		}
		// A refresh may finish after the administrator has saved another source.
		now := time.Now()
		cfg.Matchers, cfg.UpdatedAt = matchers, &now
		if err := s.db.WithContext(ctx).Model(cfg).Where("url = ?", cfg.URL).Select("Matchers", "UpdatedAt").Updates(cfg).Error; err != nil {
			logger.Warnf("无法保存刷新的 Gost 规则集 %d: %v", cfg.ID, err)
		}
	}
}

func (s *GostRulesetService) sync(ctx context.Context, unsaved map[uint]bool) {
	cfg, err := s.Get(ctx)
	if err != nil {
		return
	}
	policies := s.syncPolicies(ctx, cfg)
	nodeIDs := slices.Sorted(maps.Keys(policies))
	for id := range unsaved {
		if !slices.Contains(nodeIDs, id) {
			nodeIDs = append(nodeIDs, id)
		}
	}
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
		changed := false
		for _, name := range slices.Sorted(maps.Keys(policies[nodes[i].ID])) {
			updated, err := client.SetNamedTransparentBypass(name, policies[nodes[i].ID][name])
			if err != nil {
				logger.Warnf("同步节点 %d 的 Gost 规则集 %s 失败: %v", nodes[i].ID, name, err)
				continue
			}
			changed = changed || updated
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

func (s *GostRulesetService) syncPolicies(ctx context.Context, cfg *model.GostRuleset) map[uint]map[string][]string {
	policies := make(map[uint]map[string][]string)
	add := func(nodeID uint, name string, matchers []string) {
		if policies[nodeID] == nil {
			policies[nodeID] = make(map[string][]string)
		}
		policies[nodeID][name] = matchers
	}
	if cfg.URL != "" {
		var entryIDs []uint
		if err := s.db.WithContext(ctx).Table("tunnels").
			Distinct("tunnels.entry_node_id").
			Joins("JOIN rules ON (rules.tunnel_id = tunnels.id OR rules.id = tunnels.entry_rule_id)").
			Joins("LEFT JOIN rules AS exit_rules ON exit_rules.id = tunnels.exit_rule_id AND exit_rules.deleted_at IS NULL").
			Where(`((exit_rules.role = ? AND exit_rules.use_clash_ruleset = ?) OR
				(COALESCE(exit_rules.role, '') <> ? AND
					((tunnels.entry_rule_id IS NOT NULL AND COALESCE(tunnels.strategy, '') <> '' AND tunnels.use_clash_ruleset = ?) OR
					((tunnels.entry_rule_id IS NULL OR COALESCE(tunnels.strategy, '') = '') AND rules.use_clash_ruleset = ?))))`,
				model.EndpointRoleExit, true, model.EndpointRoleExit, true, true).
			Where("rules.status = ? AND rules.deleted_at IS NULL AND tunnels.deleted_at IS NULL", model.RuleStatusRunning).
			Pluck("tunnels.entry_node_id", &entryIDs).Error; err != nil {
			logger.Warnf("读取使用 Clash 分流的入口失败: %v", err)
		}
		for _, nodeID := range slices.Concat(cfg.NodeIDs, entryIDs) {
			add(nodeID, gost.TransparentBypassName, cfg.Matchers)
		}
	}
	var tunnels []model.GostTunnel
	if err := s.db.WithContext(ctx).Preload("EntryRule").Preload("ExitRule").
		Where("entry_rule_id IS NOT NULL AND status = ?", model.TunnelStatusRunning).Find(&tunnels).Error; err != nil {
		logger.Warnf("读取透明入口规则集选择失败: %v", err)
		return policies
	}
	for i := range tunnels {
		tunnel := &tunnels[i]
		if tunnel.EntryRule == nil {
			continue
		}
		policy := endpointTargetRule(tunnel)
		if len(policy.RulesetIDs) == 0 && len(policy.ManualDomains) == 0 {
			continue
		}
		matchers, err := s.SelectedMatchers(ctx, policy.RulesetIDs, policy.ManualDomains)
		if err != nil || len(matchers) == 0 {
			// Retain the last applied selection when a subscription is unavailable.
			logger.Warnf("同步隧道 %d 的规则集选择失败: %v", tunnel.ID, err)
			continue
		}
		add(tunnel.EntryNodeID, transparentPolicyName(tunnel.ID), matchers)
	}
	return policies
}
