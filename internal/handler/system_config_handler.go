package handler

import (
	"yagp/internal/dto"
	"yagp/internal/errors"
	"yagp/internal/service"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
)

// SystemConfigHandler 系统配置控制器
type SystemConfigHandler struct {
	systemConfigService *service.SystemConfigService
	backupService       *service.BackupService
}

// NewSystemConfigHandler 创建系统配置控制器
func NewSystemConfigHandler(sysService *service.SystemConfigService, backupService *service.BackupService) *SystemConfigHandler {
	return &SystemConfigHandler{
		systemConfigService: sysService,
		backupService:       backupService,
	}
}

// GetConfig 获取配置
func (h *SystemConfigHandler) GetConfig(c *gin.Context) {
	config, err := h.systemConfigService.GetConfig()
	if err != nil {
		response.HandleError(c, err)
		return
	}

	config.Backup.Supported = h.backupService.Supported()
	c.Header("Cache-Control", "private, no-store")
	response.Success(c, config)
}

// GetPublicConfig 获取公开配置
func (h *SystemConfigHandler) GetPublicConfig(c *gin.Context) {
	config, err := h.systemConfigService.GetPublicConfig()
	if err != nil {
		response.HandleError(c, err)
		return
	}

	response.Success(c, config)
}

// UpdateConfig 更新配置
func (h *SystemConfigHandler) UpdateConfig(c *gin.Context) {
	var input systemConfigUpdateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	req := input.configRequest()
	if err := h.systemConfigService.UpdateConfig(&req); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "配置更新成功", nil)
}

// TestEmail 发送测试邮件
func (h *SystemConfigHandler) TestEmail(c *gin.Context) {
	var req dto.EmailConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if err := h.systemConfigService.SendTestEmail(&req); err != nil {
		response.Error(c, 500, 50001, "发送失败: "+err.Error())
		return
	}

	response.SuccessWithMessage(c, "邮件发送成功", nil)
}

// TestBark 发送测试推送
func (h *SystemConfigHandler) TestBark(c *gin.Context) {
	var req dto.BarkConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "")
		return
	}

	if err := h.systemConfigService.SendTestBark(c.Request.Context(), req); err != nil {
		response.HandleError(c, err)
		return
	}

	response.SuccessWithMessage(c, "Bark 测试推送发送成功", nil)
}

// Backup 立即备份
func (h *SystemConfigHandler) Backup(c *gin.Context) {
	if err := h.backupService.CreateBackup(); err != nil {
		if err == errors.ErrBackupUnsupported {
			response.HandleError(c, err)
			return
		}
		response.Error(c, 500, 50002, "备份失败: "+err.Error())
		return
	}

	response.SuccessWithMessage(c, "备份成功", nil)
}

// Pointers distinguish omitted or null fields from explicit empty values.
type systemConfigUpdateInput struct {
	Email *struct {
		Host      *string `json:"host" binding:"required"`
		Port      *int    `json:"port" binding:"required"`
		Username  *string `json:"username" binding:"required"`
		Password  *string `json:"password" binding:"required"`
		FromEmail *string `json:"fromEmail" binding:"required"`
	} `json:"email" binding:"required"`
	Bark *struct {
		ServerURL *string `json:"serverUrl" binding:"required"`
		DeviceKey *string `json:"deviceKey" binding:"required"`
	} `json:"bark"`
	Config *struct {
		SiteTitle *string `json:"siteTitle" binding:"required"`
		LogoURL   *string `json:"logoUrl" binding:"required"`
		Copyright *string `json:"copyright" binding:"required"`
	} `json:"config" binding:"required"`
	Log *struct {
		RetentionDays *int    `json:"retentionDays" binding:"required"`
		Level         *string `json:"level" binding:"required"`
	} `json:"log" binding:"required"`
	Backup *struct {
		AutoBackup     *bool `json:"autoBackup" binding:"required"`
		RetentionCount *int  `json:"retentionCount" binding:"required"`
	} `json:"backup" binding:"required"`
}

func (input systemConfigUpdateInput) configRequest() dto.UpdateSystemConfigReq {
	req := dto.UpdateSystemConfigReq{
		Email: dto.EmailConfigReq{
			Host:      *input.Email.Host,
			Port:      *input.Email.Port,
			Username:  *input.Email.Username,
			Password:  *input.Email.Password,
			FromEmail: *input.Email.FromEmail,
		},
		Config: dto.PanelSettingReq{
			SiteTitle: *input.Config.SiteTitle,
			LogoURL:   *input.Config.LogoURL,
			Copyright: *input.Config.Copyright,
		},
		Log: dto.LogConfigReq{
			RetentionDays: *input.Log.RetentionDays,
			Level:         *input.Log.Level,
		},
		Backup: dto.BackupConfigReq{
			AutoBackup:     *input.Backup.AutoBackup,
			RetentionCount: *input.Backup.RetentionCount,
		},
	}
	if input.Bark != nil {
		req.Bark = &dto.BarkConfig{
			ServerURL: *input.Bark.ServerURL,
			DeviceKey: *input.Bark.DeviceKey,
		}
	}
	return req
}
