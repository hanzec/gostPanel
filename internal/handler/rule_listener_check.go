package handler

import (
	"net/http"
	"strconv"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type listenerCheckResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// CheckListener probes the binding saved for this entrance on its node.
func (h *RuleHandler) CheckListener(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的入口 ID")
		return
	}
	rule, err := h.ruleService.GetByID(uint(id))
	if err != nil {
		response.HandleError(c, err)
		return
	}
	if rule.Node == nil {
		response.BadRequest(c, "入口没有独立的监听节点")
		return
	}
	if rule.Status == model.RuleStatusRunning {
		response.Error(c, http.StatusConflict, response.CodeBadRequest, "入口已在运行，请停止后再检查监听地址")
		return
	}
	if rule.Node.Status == model.NodeStatusOffline {
		response.HandleError(c, errors.ErrNodeOffline)
		return
	}

	reply, err := utils.GetGostClient(rule.Node).CheckListener(c.Request.Context(), rule.Protocol.ListenerNetwork(), rule.ListenIP, uint32(rule.ListenPort))
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			response.Error(c, http.StatusNotImplemented, response.CodeInternalError, "请升级节点的 yagp-daemon 后再检查监听地址")
			return
		}
		nodeOperationError(c, err)
		return
	}
	response.Success(c, listenerCheckResult{Available: reply.GetAvailable(), Reason: reply.GetReason()})
}
