package utils

import (
	"net"
	"strconv"
	"strings"
	"time"

	"ycgp/internal/config"
	"ycgp/internal/model"
	"ycgp/pkg/gost"
)

// GetGostClient 根据节点配置创建 Gost 客户端
func GetGostClient(node *model.GostNode) *gost.Client {
	return gost.NewClient(&gost.Config{
		Address:  net.JoinHostPort(strings.Trim(node.Address, "[]"), strconv.Itoa(node.Port)),
		CAFile:   config.Get().NodeTLS.CAFile,
		Username: node.Username,
		Password: node.Password,
		Timeout:  5 * time.Second,
	})
}
