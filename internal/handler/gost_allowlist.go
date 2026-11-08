package handler

import (
	"yagp/internal/service"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

type GostAllowlistHandler struct {
	service *service.GostAllowlistService
}

func NewGostAllowlistHandler(service *service.GostAllowlistService) *GostAllowlistHandler {
	return &GostAllowlistHandler{service: service}
}

func (h *GostAllowlistHandler) Get(c *gin.Context) {
	config, err := h.service.Get(c.Request.Context())
	if err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, config)
}

func (h *GostAllowlistHandler) Update(c *gin.Context) {
	var req struct {
		NodeIDs []uint `json:"node_ids" binding:"required,dive,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请提供白名单节点 ID 列表")
		return
	}
	config, err := h.service.Update(c.Request.Context(), req.NodeIDs)
	if err != nil {
		response.HandleError(c, err)
		return
	}
	response.Success(c, config)
}
