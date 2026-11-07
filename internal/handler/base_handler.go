package handler

import (
	"github.com/gin-gonic/gin"
)

// RequestContext 请求上下文信息
// 封装从 Gin Context 中提取的用户信息和请求元数据
type RequestContext struct {
	Username  string // 用户名
	ClientIP  string // 客户端 IP
	UserAgent string // User-Agent
}

// GetRequestContext 从 Gin Context 中提取请求上下文信息
// 用于审计日志记录和操作追踪
func GetRequestContext(c *gin.Context) *RequestContext {
	return &RequestContext{
		Username:  c.GetString("username"),
		ClientIP:  c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
	}
}
