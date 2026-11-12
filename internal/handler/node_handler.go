package handler

import (
	"net/http"
	"strconv"
	"time"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/geoip"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

// NodeHandler 节点控制器
// 处理节点相关的 HTTP 请求
type NodeHandler struct {
	nodeService    *service.NodeService
	upgradeService *service.NodeUpgradeService
	geoIP          *geoip.Database
	geoIPClient    *http.Client
}

// NewNodeHandler 创建节点控制器
func NewNodeHandler(nodeService *service.NodeService, upgradeService *service.NodeUpgradeService, geoIP *geoip.Database) *NodeHandler {
	return &NodeHandler{nodeService: nodeService, upgradeService: upgradeService, geoIP: geoIP, geoIPClient: http.DefaultClient}
}

// Create 创建节点
func (h *NodeHandler) Create(c *gin.Context) {
	var req dto.CreateNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := GetRequestContext(c)

	node, err := h.nodeService.Create(&req, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, node)
}

func (h *NodeHandler) IssueRegistrationToken(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的节点 ID")
		return
	}
	ctx := GetRequestContext(c)
	token, err := h.nodeService.IssueRegistrationToken(c.Request.Context(), uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, token)
}

func (h *NodeHandler) RevokeCredentials(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的节点 ID")
		return
	}
	if err := h.nodeService.RevokeCredentials(c.Request.Context(), uint(id)); err != nil {
		response.HandleError(c, err)
		return
	}
	operator := GetRequestContext(c)
	h.nodeService.RecordOperation(uint(id), "吊销节点通信凭据", operator.Username, operator.ClientIP, operator.UserAgent)
	response.Success(c, nil)
}

// Update 更新节点
func (h *NodeHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的节点 ID")
		return
	}

	var req dto.UpdateNodeReq
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ctx := GetRequestContext(c)

	node, err := h.nodeService.Update(uint(id), &req, ctx.Username, ctx.ClientIP, ctx.UserAgent)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	h.setNodeRegions(c.Request.Context(), node)
	response.Success(c, node)
}

// Delete 删除节点
func (h *NodeHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的节点 ID")
		return
	}

	ctx := GetRequestContext(c)

	if err = h.nodeService.Delete(uint(id), ctx.Username, ctx.ClientIP, ctx.UserAgent); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "删除成功", nil)
}

// GetByID 获取节点详情
func (h *NodeHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的节点 ID")
		return
	}

	node, err := h.nodeService.GetByID(uint(id))
	if err != nil {
		response.HandleError(c, err)
		return
	}

	h.setNodeRegions(c.Request.Context(), node)
	response.Success(c, withCurrentTrafficUsage(*node, time.Now()))
}

// List 获取节点列表
func (h *NodeHandler) List(c *gin.Context) {
	var req dto.NodeListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	nodes, total, err := h.nodeService.List(&req)
	if err != nil {
		response.HandleError(c, err)
		return
	}

	now := time.Now()
	items := make([]nodeTrafficResponse, len(nodes))
	regionTargets := make([]*model.GostNode, len(nodes))
	for i, node := range nodes {
		items[i] = withCurrentTrafficUsage(node, now)
		regionTargets[i] = &items[i].GostNode
	}
	h.setNodeRegions(c.Request.Context(), regionTargets...)
	response.SuccessPage(c, items, total, req.Page, req.PageSize)
}

// GetConfig 获取节点配置
func (h *NodeHandler) GetConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.BadRequest(c, "无效的节点 ID")
		return
	}

	config, err := h.nodeService.GetConfig(uint(id))
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, config)
}
