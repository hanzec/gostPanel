package handler

import (
	"errors"
	"strconv"
	"time"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GostRulesetHandler struct {
	service *service.GostRulesetService
}

func NewGostRulesetHandler(service *service.GostRulesetService) *GostRulesetHandler {
	return &GostRulesetHandler{service: service}
}

func (h *GostRulesetHandler) Get(c *gin.Context) {
	ruleset, err := h.service.Get(c.Request.Context())
	if err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, ruleset)
}

func (h *GostRulesetHandler) Update(c *gin.Context) {
	var req struct {
		URL     *string `json:"url" binding:"required,max=4096"`
		NodeIDs []uint  `json:"node_ids" binding:"required,max=500"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "规则集配置无效")
		return
	}
	ruleset, err := h.service.Update(c.Request.Context(), *req.URL, req.NodeIDs)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, ruleset)
}

type gostRulesetSummary struct {
	ID           uint       `json:"id"`
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	MatcherCount int        `json:"matcher_count"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

type gostSubscriptionRequest struct {
	Name string `json:"name" binding:"required,max=100"`
	URL  string `json:"url" binding:"required,max=4096"`
}

func (h *GostRulesetHandler) List(c *gin.Context) {
	rulesets, err := h.service.List(c.Request.Context())
	if err != nil {
		response.HandleError(c, err)
		return
	}
	list := make([]gostRulesetSummary, 0, len(rulesets))
	for _, ruleset := range rulesets {
		list = append(list, summarizeGostRuleset(ruleset))
	}
	response.Success(c, list)
}

func (h *GostRulesetHandler) Create(c *gin.Context) {
	var req gostSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请填写规则集名称和 HTTPS 订阅链接")
		return
	}
	ruleset, err := h.service.Create(c.Request.Context(), req.Name, req.URL)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, summarizeGostRuleset(*ruleset))
}

func (h *GostRulesetHandler) UpdateByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "规则集 ID 无效")
		return
	}
	var req gostSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请填写规则集名称和 HTTPS 订阅链接")
		return
	}
	ruleset, err := h.service.UpdateByID(c.Request.Context(), uint(id), req.Name, req.URL)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "规则集不存在")
		return
	}
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, summarizeGostRuleset(*ruleset))
}

func (h *GostRulesetHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "规则集 ID 无效")
		return
	}
	err = h.service.Delete(c.Request.Context(), uint(id))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "规则集不存在")
		return
	}
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, nil)
}

func summarizeGostRuleset(ruleset model.GostRuleset) gostRulesetSummary {
	name := ruleset.Name
	if ruleset.ID == 1 && name == "" {
		name = "默认规则集"
	}
	return gostRulesetSummary{ID: ruleset.ID, Name: name, URL: ruleset.URL, MatcherCount: len(ruleset.Matchers), UpdatedAt: ruleset.UpdatedAt}
}
