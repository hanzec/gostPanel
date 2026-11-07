package utils

import (
	"net"
	"strconv"
	"strings"
	"time"

	"yagp/internal/model"
	"yagp/pkg/gost"
)

// GetGostClient 根据节点配置创建 Gost 客户端
func GetGostClient(node *model.GostNode) *gost.Client {
	return gost.NewClient(&gost.Config{
		Address:        net.JoinHostPort(strings.Trim(node.Address, "[]"), strconv.Itoa(node.Port)),
		CertificatePEM: node.TLSCertificatePEM,
		Username:       node.Username,
		Password:       node.Password,
		Timeout:        5 * time.Second,
	})
}
