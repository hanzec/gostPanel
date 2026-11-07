package handler

import (
	"fmt"
	"net/http"

	"yagp/pkg/nodegrpc"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

type nodeNftConfig struct {
	Config   string `json:"config"`
	Template string `json:"template"`
	Table    string `json:"table"`
}

func nftTemplate(table string) string {
	return fmt.Sprintf(`{
  "nftables": [
    {
      "add": {
        "chain": {
          "family": "inet",
          "table": "%s",
          "name": "prerouting",
          "type": "nat",
          "hook": "prerouting",
          "prio": -100,
          "policy": "accept"
        }
      }
    }
  ]
}`, table)
}

func nftConfigResponse(id uint, config string) nodeNftConfig {
	table := nodegrpc.NftTableName(uint64(id))
	return nodeNftConfig{Config: config, Table: table, Template: nftTemplate(table)}
}

func (h *NodeHandler) GetNftConfig(c *gin.Context) {
	client, id := h.nodeClient(c)
	if client == nil {
		return
	}
	config, err := client.GetNftConfig(c.Request.Context())
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	response.Success(c, nftConfigResponse(id, config.GetConfig()))
}

func (h *NodeHandler) ApplyNftConfig(c *gin.Context) {
	client, id := h.nodeClient(c)
	if client == nil {
		return
	}
	// An escaped config string may take up to six bytes per input byte in the JSON body.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 6*nodegrpc.MaxNftConfigBytes+1024)
	var request struct {
		Config string `json:"config" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || len(request.Config) > nodegrpc.MaxNftConfigBytes {
		response.BadRequest(c, "nft 配置必须是非空文本，最大 256 KiB")
		return
	}
	config, err := client.ApplyNftConfig(c.Request.Context(), request.Config)
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	h.recordOperation(c, id, "应用 nftables 配置")
	response.Success(c, nftConfigResponse(id, config.GetConfig()))
}
