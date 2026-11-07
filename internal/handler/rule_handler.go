package handler

import (
	"strconv"

	"yagp/internal/dto"
	"yagp/internal/service"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

// RuleHandler 规则控制器
// 处理规则相关的 HTTP 请求
type RuleHandler struct {
	ruleService *service.RuleService
}

// NewRuleHandler 创建规则控制器
func NewRuleHandler(ruleService *service.RuleService) *RuleHandler {
	return &RuleHandler{ruleService: ruleService}
}

// Create 创建规则
func (h *RuleHandler) Create(c *gin.Context) {
	var req dto.CreateRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := GetRequestContext(c)

	rule, err := h.ruleService.Create(&req, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, rule)
}

// Update 更新规则
func (h *RuleHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的规则 ID")
		return
	}

	var req dto.UpdateRuleReq
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := GetRequestContext(c)

	rule, err := h.ruleService.Update(uint(id), &req, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, rule)
}

// Delete 删除规则
func (h *RuleHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的规则 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.ruleService.Delete(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "删除成功", nil)
}

// GetByID 获取规则详情
func (h *RuleHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的规则 ID")
		return
	}

	rule, err := h.ruleService.GetByID(uint(id))
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, rule)
}

// List 获取规则列表
func (h *RuleHandler) List(c *gin.Context) {
	var req dto.RuleListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	rules, total, err := h.ruleService.List(&req)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessPage(c, rules, total, req.Page, req.PageSize)
}

// Start 启动规则
func (h *RuleHandler) Start(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的规则 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.ruleService.Start(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "启动成功", nil)
}

// Stop 停止规则
func (h *RuleHandler) Stop(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的规则 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.ruleService.Stop(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "停止成功", nil)
}
