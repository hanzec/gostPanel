package middleware

import (
	"time"

	"yagp/pkg/logger"

	"github.com/gin-gonic/gin"
)

// Logger 请求日志中间件
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 开始时间
		startTime := time.Now()

		// 处理请求
		c.Next()

		// 结束时间
		endTime := time.Now()

		// 执行时间
		latency := endTime.Sub(startTime)

		// 请求信息
		clientIP := c.ClientIP()
		method := c.Request.Method
		path := c.Request.URL.Path
		statusCode := c.Writer.Status()
		bodySize := c.Writer.Size()

		// 记录日志
		const logFormat = "[%d] %s %s - %s - %v - %d bytes"
		if reason := c.GetString(authentikFailureKey); reason != "" {
			logger.WithFields(map[string]any{
				"auth_failure": reason,
				"remote_addr":  c.Request.RemoteAddr,
			}).Warnf(logFormat, statusCode, method, path, clientIP, latency, bodySize)
		} else if statusCode >= 400 {
			logger.Warnf(logFormat,
				statusCode, method, path, clientIP, latency, bodySize)
		} else {
			logger.Infof(logFormat,
				statusCode, method, path, clientIP, latency, bodySize)
		}
	}
}
