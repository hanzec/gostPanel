package handler

import (
	"ycgp/pkg/response"

	"github.com/gin-gonic/gin"
)

func CurrentAdmin(c *gin.Context) {
	response.Success(c, gin.H{
		"username": c.GetString("username"),
		"role":     "admin",
	})
}
