package handler

import (
	"yagp/internal/service"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
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
