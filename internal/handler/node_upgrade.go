package handler

import (
	"strconv"

	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

func (h *NodeHandler) GetUpgrade(c *gin.Context) {
	id := upgradeNodeID(c)
	if id == 0 {
		return
	}
	state, err := h.upgradeService.Get(c.Request.Context(), id)
	if err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, state)
}

func (h *NodeHandler) SetUpgradePolicy(c *gin.Context) {
	id := upgradeNodeID(c)
	if id == 0 {
		return
	}
	var request struct {
		AutoUpgradeDaemon *bool `json:"auto_upgrade_daemon" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "请提供 yagp-daemon 的自动升级开关")
		return
	}
	if err := h.upgradeService.SetPolicy(c.Request.Context(), id, *request.AutoUpgradeDaemon); err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *NodeHandler) StartUpgrade(c *gin.Context) {
	id := upgradeNodeID(c)
	if id == 0 {
		return
	}
	state, err := h.upgradeService.Upgrade(c.Request.Context(), id, c.Param("component"))
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	h.recordOperation(c, id, "请求升级 "+state.GetComponent()+" 到 "+state.GetTargetVersion())
	response.Success(c, state)
}

func upgradeNodeID(c *gin.Context) uint {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的节点 ID")
		return 0
	}
	return uint(id)
}
