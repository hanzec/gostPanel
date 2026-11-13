package handler

import (
	"strconv"

	"yagp/internal/dto"
	"yagp/internal/errors"
	"yagp/internal/service"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

// TunnelHandler 隧道控制器
// 处理多跳隧道相关的 HTTP 请求
type TunnelHandler struct {
	tunnelService *service.TunnelService
}

// NewTunnelHandler 创建隧道控制器
func NewTunnelHandler(tunnelService *service.TunnelService) *TunnelHandler {
	return &TunnelHandler{tunnelService: tunnelService}
}

// Create 创建隧道
func (h *TunnelHandler) Create(c *gin.Context) {
	var req dto.CreateTunnelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := GetRequestContext(c)

	tunnel, err := h.tunnelService.Create(&req, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, tunnel)
}

// Update 更新隧道
func (h *TunnelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}

	var req dto.UpdateTunnelReq
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := GetRequestContext(c)

	tunnel, err := h.tunnelService.Update(uint(id), &req, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, tunnel)
}

// UpdateAllowlist 保存隧道出口 Relay 的来源 IP 白名单。
func (h *TunnelHandler) UpdateAllowlist(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}

	var req dto.UpdateTunnelAllowlistReq
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请提供白名单节点 ID 列表")
		return
	}

	ctx := GetRequestContext(c)
	tunnel, err := h.tunnelService.UpdateAllowlist(c.Request.Context(), uint(id), req.AllowlistNodeIDs, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, tunnel)
}

// Delete 删除隧道
func (h *TunnelHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.tunnelService.Delete(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "删除成功", nil)
}

// GetByID 获取隧道详情
func (h *TunnelHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}

	tunnel, err := h.tunnelService.GetByID(uint(id))
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, tunnel)
}

// List 获取隧道列表
func (h *TunnelHandler) List(c *gin.Context) {
	var req dto.TunnelListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	tunnels, total, err := h.tunnelService.List(&req)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessPage(c, tunnels, total, req.Page, req.PageSize)
}

// Start 启动隧道
func (h *TunnelHandler) Start(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.tunnelService.Start(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "启动成功", nil)
}

// Stop 停止隧道
func (h *TunnelHandler) Stop(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.tunnelService.Stop(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "停止成功", nil)
}

func (h *TunnelHandler) Test(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的隧道 ID")
		return
	}
	result, err := h.tunnelService.Test(c.Request.Context(), uint(id))
	if err != nil {
		if businessErr, ok := err.(*errors.BizError); ok && result != nil {
			c.JSON(businessErr.HTTPCode, response.Response{Code: businessErr.Code, Message: businessErr.Message, Data: result})
			return
		}
		response.HandleError(c, err)
		return
	}
	response.Success(c, result)
}
