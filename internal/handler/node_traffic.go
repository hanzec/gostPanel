package handler

import (
	"fmt"
	"math"
	"strconv"

	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

func (h *NodeHandler) CalibrateTraffic(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的节点 ID")
		return
	}
	var request struct {
		InputBytes     *int64  `json:"input_bytes"`
		OutputBytes    *int64  `json:"output_bytes"`
		QuotaUsedBytes *uint64 `json:"quota_used_bytes"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "流量必须为整数个字节")
		return
	}
	ctx := c.Request.Context()
	var details string
	if request.QuotaUsedBytes != nil {
		if request.InputBytes != nil || request.OutputBytes != nil || *request.QuotaUsedBytes > math.MaxInt64 {
			response.BadRequest(c, "请单独校准配额用量，并填写有效的字节数")
			return
		}
		err = h.nodeService.CalibrateNodeQuota(ctx, uint(id), *request.QuotaUsedBytes)
		details = fmt.Sprintf("校准当前周期配额用量为 %d 字节", *request.QuotaUsedBytes)
	} else {
		if request.InputBytes == nil || request.OutputBytes == nil || *request.InputBytes < 0 || *request.OutputBytes < 0 ||
			*request.InputBytes > math.MaxInt64-*request.OutputBytes {
			response.BadRequest(c, "请填写有效的累计入站和出站字节数")
			return
		}
		err = h.nodeService.CalibrateNodeTotals(ctx, uint(id), *request.InputBytes, *request.OutputBytes)
		details = fmt.Sprintf("校准累计流量：入站 %d 字节，出站 %d 字节", *request.InputBytes, *request.OutputBytes)
	}
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	h.recordOperation(c, uint(id), details)
	response.SuccessWithMessage(c, "流量已校准", nil)
}
