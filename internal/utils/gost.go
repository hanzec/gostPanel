package utils

import (
	"time"

	"yagp/internal/model"
	"yagp/pkg/gost"
)

// GetGostClient 根据节点配置创建 Gost 客户端
func GetGostClient(node *model.GostNode) *gost.Client {
	return gost.NewClient(&gost.Config{
		NodeID:   node.ID,
		Username: node.Username,
		Password: node.Password,
		Timeout:  5 * time.Second,
	})
}
