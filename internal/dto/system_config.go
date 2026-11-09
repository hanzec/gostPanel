package dto

// ==================== 系统配置相关 ====================

// SystemConfigResp 系统配置响应
type SystemConfigResp struct {
	Email  EmailConfigResp  `json:"email"`
	Bark   BarkConfig       `json:"bark"`
	Config PanelSettingResp `json:"config"`
	Log    LogConfigResp    `json:"log"`
	Backup BackupConfigResp `json:"backup"`
}

type PublicSystemConfigResp struct {
	SiteTitle     string `json:"siteTitle"`
	LogoURL       string `json:"logoUrl"`
	Copyright     string `json:"copyright"`
	Host          string `json:"host"`
	DaemonVersion string `json:"daemonVersion,omitempty"`
}

type EmailConfigResp struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	FromEmail string `json:"fromEmail"`
}

type BarkConfig struct {
	ServerURL string `json:"serverUrl"`
	DeviceKey string `json:"deviceKey"`
}

type PanelSettingResp struct {
	SiteTitle string `json:"siteTitle"`
	LogoURL   string `json:"logoUrl"`
	Copyright string `json:"copyright"`
}

type LogConfigResp struct {
	RetentionDays int    `json:"retentionDays"`
	Level         string `json:"level"`
}

type BackupConfigResp struct {
	Supported      bool `json:"supported"`
	AutoBackup     bool `json:"autoBackup"`
	RetentionCount int  `json:"retentionCount"`
}

// UpdateSystemConfigReq 更新系统配置请求
type UpdateSystemConfigReq struct {
	Email  EmailConfigReq  `json:"email"`
	Bark   *BarkConfig     `json:"bark"`
	Config PanelSettingReq `json:"config"`
	Log    LogConfigReq    `json:"log"`
	Backup BackupConfigReq `json:"backup"`
}

type EmailConfigReq struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	FromEmail string `json:"fromEmail"`
	ToEmail   string `json:"toEmail"` // 测试邮件接收人
}

type PanelSettingReq struct {
	SiteTitle string `json:"siteTitle"`
	LogoURL   string `json:"logoUrl"`
	Copyright string `json:"copyright"`
}

type LogConfigReq struct {
	RetentionDays int    `json:"retentionDays"`
	Level         string `json:"level"`
}

type BackupConfigReq struct {
	AutoBackup     bool `json:"autoBackup"`
	RetentionCount int  `json:"retentionCount"`
}
