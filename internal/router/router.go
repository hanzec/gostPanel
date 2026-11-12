package router

import (
	"yagp/internal/buildinfo"
	"yagp/internal/config"
	"yagp/internal/handler"
	"yagp/internal/middleware"
	"yagp/internal/repository"
	"yagp/internal/service"
	"yagp/pkg/geoip"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Router 路由配置
type Router struct {
	db                      *gorm.DB
	adminAuth               gin.HandlerFunc
	nodeUpgrade             *service.NodeUpgradeService
	registrationFingerprint string
	geoIP                   *geoip.Database
}

// NewRouter 创建路由实例
func NewRouter(db *gorm.DB, cfg config.AuthentikConfig, nodeUpgrade *service.NodeUpgradeService, registrationFingerprint string, geoIP *geoip.Database) (*Router, error) {
	adminAuth, err := middleware.NewAuthentikAuth(cfg)
	if err != nil {
		return nil, err
	}
	return &Router{
		db:                      db,
		adminAuth:               adminAuth,
		nodeUpgrade:             nodeUpgrade,
		registrationFingerprint: registrationFingerprint,
		geoIP:                   geoIP,
	}, nil
}

// Setup 配置路由
func (r *Router) Setup(engine *gin.Engine) {
	// 全局中间件
	engine.Use(middleware.Logger())
	engine.Use(middleware.Recovery())
	engine.Use(middleware.ErrorHandler())

	// 健康检查
	engine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// API v1 路由组
	apiV1 := engine.Group("/api/v1")

	// API v1 健康检查
	apiV1.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "version": buildinfo.Version})
	})

	// 初始化服务
	nodeService := service.NewNodeService(r.db, r.registrationFingerprint)
	ruleService := service.NewRuleService(r.db)
	tunnelService := service.NewTunnelService(r.db)
	statsService := service.NewStatsService(r.db)
	logService := service.NewLogService(r.db)

	// 初始化系统配置
	systemConfigRepo := repository.NewSystemConfigRepository(r.db)
	systemConfigService := service.NewSystemConfigService(systemConfigRepo)
	backupService := service.NewBackupService(r.db)

	// 初始化控制器
	nodeHandler := handler.NewNodeHandler(nodeService, r.nodeUpgrade, r.geoIP)
	ruleHandler := handler.NewRuleHandler(ruleService)
	tunnelHandler := handler.NewTunnelHandler(tunnelService)
	statsHandler := handler.NewStatsHandler(statsService)
	logHandler := handler.NewLogHandler(logService)
	systemConfigHandler := handler.NewSystemConfigHandler(systemConfigService, backupService)
	gostRulesetHandler := handler.NewGostRulesetHandler(service.NewGostRulesetService(r.db))

	// 管理员路由
	authRoutes := apiV1.Group("")
	authRoutes.Use(r.adminAuth)
	{
		authRoutes.GET("/auth/info", handler.CurrentAdmin)

		// 仪表盘统计
		authRoutes.GET("/dashboard/stats", statsHandler.GetDashboard)

		// 节点管理
		authRoutes.GET("/nodes", nodeHandler.List)
		authRoutes.GET("/nodes/:id", nodeHandler.GetByID)
		authRoutes.POST("/nodes", nodeHandler.Create)
		authRoutes.POST("/nodes/:id/registration-token", nodeHandler.IssueRegistrationToken)
		authRoutes.DELETE("/nodes/:id/credentials", nodeHandler.RevokeCredentials)
		authRoutes.PUT("/nodes/:id", nodeHandler.Update)
		authRoutes.PUT("/nodes/:id/traffic", nodeHandler.CalibrateTraffic)
		authRoutes.DELETE("/nodes/:id", nodeHandler.Delete)
		authRoutes.GET("/nodes/:id/config", nodeHandler.GetConfig)
		authRoutes.GET("/nodes/:id/upgrade", nodeHandler.GetUpgrade)
		authRoutes.PUT("/nodes/:id/upgrade", nodeHandler.SetUpgradePolicy)
		authRoutes.POST("/nodes/:id/upgrade/:component", nodeHandler.StartUpgrade)
		authRoutes.GET("/nodes/:id/gost/service", nodeHandler.GostServiceStatus)
		authRoutes.POST("/nodes/:id/gost/restart", nodeHandler.RestartGostService)
		authRoutes.GET("/nodes/:id/gost/logs", nodeHandler.GostLogs)
		authRoutes.GET("/nodes/:id/nft", nodeHandler.GetNftConfig)

		// 规则管理
		authRoutes.GET("/rules", ruleHandler.List)
		authRoutes.GET("/rules/:id", ruleHandler.GetByID)
		authRoutes.POST("/rules", ruleHandler.Create)
		authRoutes.PUT("/rules/:id", ruleHandler.Update)
		authRoutes.DELETE("/rules/:id", ruleHandler.Delete)
		authRoutes.POST("/rules/:id/start", ruleHandler.Start)
		authRoutes.POST("/rules/:id/stop", ruleHandler.Stop)
		authRoutes.POST("/rules/:id/listener-check", ruleHandler.CheckListener)

		// 隧道管理
		authRoutes.GET("/tunnels", tunnelHandler.List)
		authRoutes.GET("/tunnels/:id", tunnelHandler.GetByID)
		authRoutes.POST("/tunnels", tunnelHandler.Create)
		authRoutes.PUT("/tunnels/:id", tunnelHandler.Update)
		authRoutes.PUT("/tunnels/:id/allowlist", tunnelHandler.UpdateAllowlist)
		authRoutes.DELETE("/tunnels/:id", tunnelHandler.Delete)
		authRoutes.POST("/tunnels/:id/start", tunnelHandler.Start)
		authRoutes.POST("/tunnels/:id/stop", tunnelHandler.Stop)
		authRoutes.POST("/tunnels/:id/test", tunnelHandler.Test)

		// 操作日志
		authRoutes.GET("/logs", logHandler.List)

		// 系统设置
		authRoutes.GET("/system/public-config", systemConfigHandler.GetPublicConfig)
		authRoutes.GET("/system/config", systemConfigHandler.GetConfig)
		authRoutes.PUT("/system/config", systemConfigHandler.UpdateConfig)
		authRoutes.GET("/system/gost-ruleset", gostRulesetHandler.Get)
		authRoutes.PUT("/system/gost-ruleset", gostRulesetHandler.Update)
		authRoutes.GET("/system/gost-rulesets", gostRulesetHandler.List)
		authRoutes.POST("/system/gost-rulesets", gostRulesetHandler.Create)
		authRoutes.PUT("/system/gost-rulesets/:id", gostRulesetHandler.UpdateByID)
		authRoutes.DELETE("/system/gost-rulesets/:id", gostRulesetHandler.Delete)
		authRoutes.POST("/system/email/test", systemConfigHandler.TestEmail)
		authRoutes.POST("/system/bark/test", systemConfigHandler.TestBark)
		authRoutes.POST("/system/backup", systemConfigHandler.Backup)
	}

	// 静态文件
	r.setupStatic(engine)
}
