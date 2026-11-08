package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"yagp/pkg/gost"
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

func loopbackNftTemplate(table string, port uint64) string {
	return fmt.Sprintf(`{
  "nftables": [
    {"add":{"chain":{
      "family":"inet","table":"%[1]s","name":"loopback_%[2]d",
      "type":"nat","hook":"output","prio":-100,"policy":"accept"
    }}},
    {"add":{"rule":{
      "family":"inet","table":"%[1]s","chain":"loopback_%[2]d",
      "expr":[
        {"match":{"op":"==","left":{"meta":{"key":"nfproto"}},"right":"ipv4"}},
        {"match":{"op":"==","left":{"meta":{"key":"skgid"}},"right":65534}},
        {"match":{"op":"!=","left":{"meta":{"key":"mark"}},"right":%[3]d}},
        {"match":{"op":"!=","left":{"payload":{"protocol":"ip","field":"daddr"}},"right":{"prefix":{"addr":"127.0.0.0","len":8}}}},
        {"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":%[2]d}},
        {"redirect":{"port":%[2]d}}
      ]
    }}}
  ]
}`, table, port, gost.ClashRedirectMark)
}

func (h *NodeHandler) GetNftConfig(c *gin.Context) {
	client, id := h.nodeClient(c)
	if client == nil {
		return
	}
	var loopbackPort uint64
	if value, ok := c.GetQuery("loopback_port"); ok {
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil || port == 0 {
			response.BadRequest(c, "回环监听端口必须在 1 到 65535 之间")
			return
		}
		loopbackPort = port
	}
	config, err := client.GetNftConfig(c.Request.Context())
	if err != nil {
		nodeOperationError(c, err)
		return
	}
	result := nftConfigResponse(id, config.GetConfig())
	if loopbackPort > 0 {
		result.Template = loopbackNftTemplate(result.Table, loopbackPort)
	}
	response.Success(c, result)
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
