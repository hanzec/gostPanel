package handler

import (
	"net/http"
	"strconv"
	"strings"

	"yagp/pkg/gost"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *NodeHandler) GostServiceStatus(c *gin.Context) {
	client, _ := h.nodeClient(c)
	if client == nil {
		return
	}
	state, err := client.GostServiceStatus(c.Request.Context())
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	response.Success(c, state)
}

func (h *NodeHandler) RestartGostService(c *gin.Context) {
	client, id := h.nodeClient(c)
	if client == nil {
		return
	}
	state, err := client.RestartGostService(c.Request.Context())
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	h.recordOperation(c, id, "重启 Gost 服务")
	response.Success(c, state)
}

func (h *NodeHandler) GostLogs(c *gin.Context) {
	client, _ := h.nodeClient(c)
	if client == nil {
		return
	}
	logs, err := client.GostLogs(c.Request.Context())
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	response.Success(c, logs)
}

func (h *NodeHandler) nodeClient(c *gin.Context) (*gost.Client, uint) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.BadRequest(c, "无效的节点 ID")
		return nil, 0
	}
	client, err := h.nodeService.CreateGostClient(uint(id))
	if err != nil {
		response.HandleError(c, err)
		return nil, 0
	}
	return client, uint(id)
}

func (h *NodeHandler) recordOperation(c *gin.Context, id uint, details string) {
	operator := GetRequestContext(c)
	h.nodeService.RecordOperation(id, details, operator.Username, operator.ClientIP, operator.UserAgent)
}

func nodeOperationError(c *gin.Context, err error) {
	rpc := status.Convert(err)
	switch rpc.Code() {
	case codes.InvalidArgument, codes.ResourceExhausted:
		response.BadRequest(c, rpc.Message())
	case codes.Unimplemented:
		message := rpc.Message()
		if strings.HasPrefix(message, "unknown method ") {
			message = "节点版本不支持该运维操作，请升级节点桥"
		}
		response.Error(c, http.StatusNotImplemented, response.CodeInternalError, message)
	case codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied:
		response.Error(c, http.StatusConflict, response.CodeBadRequest, rpc.Message())
	case codes.DeadlineExceeded:
		response.Error(c, http.StatusGatewayTimeout, response.CodeInternalError, "节点操作超时，请刷新状态确认结果")
	case codes.Unavailable, codes.Unauthenticated:
		response.Error(c, http.StatusBadGateway, response.CodeInternalError, "无法通过认证连接节点，请检查节点地址、证书和运行状态")
	default:
		response.HandleError(c, err)
	}
}
