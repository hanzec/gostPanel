package handler

import (
	"fmt"

	"yagp/pkg/nodegrpc"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

type nodeNftConfig struct {
	Config string `json:"config"`
	Table  string `json:"table"`
	Live   bool   `json:"live"`
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
	table := config.GetTable()
	if table != nodegrpc.SingleNodeNftTable {
		table = nodegrpc.NftTableName(uint64(id))
	}
	current := config.GetLiveConfig()
	live := current != ""
	if !live {
		current = config.GetConfig()
	}
	response.Success(c, nodeNftConfig{Config: current, Table: table, Live: live})
}
