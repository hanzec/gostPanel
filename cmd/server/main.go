package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"yagp/internal/buildinfo"
	"yagp/internal/config"
	"yagp/internal/handler"
	"yagp/internal/model"
	"yagp/internal/router"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	// 解析命令行参数
	var configPath string
	flag.StringVar(&configPath, "c", "", "配置文件路径")
	flag.StringVar(&configPath, "config", "", "配置文件路径")
	showVersion := flag.Bool("version", false, "打印构建版本")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	// 加载配置
	cfg, err := config.Load(configPath)
	if err != nil {
		panic("加载配置失败: " + err.Error())
	}

	// 初始化日志
	if err = logger.Init(&logger.Config{
		Level:  cfg.Log.Level,
		Format: cfg.Log.Format,
		Output: cfg.Log.Output,
	}); err != nil {
		panic("初始化日志失败: " + err.Error())
	}
	defer func() {
		_ = logger.Sync()
	}()

	logger.Info("yagp 启动中...")

	// 设置 Gin 模式
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	// 初始化数据库
	db, err := initDatabase(cfg)
	if err != nil {
		logger.Fatalf("初始化数据库失败: %v", err)
	}
	logger.Info("数据库初始化完成")

	// 自动迁移
	if err = autoMigrate(db); err != nil {
		logger.Fatalf("数据库迁移失败: %v", err)
	}
	logger.Info("数据库迁移完成")

	// 初始化系统配置
	if err = initSystemConfig(db); err != nil {
		logger.Fatalf("初始化系统配置失败: %v", err)
	}

	engine := gin.New()
	if err = engine.SetTrustedProxies(cfg.Authentik.TrustedProxies); err != nil {
		logger.Fatalf("反向代理配置无效: %v", err)
	}

	nodeUpgrade := service.NewNodeUpgradeService(db)
	r, err := router.NewRouter(db, cfg.Authentik, nodeUpgrade)
	if err != nil {
		logger.Fatalf("Authentik 配置无效: %v", err)
	}
	r.Setup(engine)

	reportServer, err := startReports(cfg.Report, db)
	if err != nil {
		logger.Fatalf("启动流量上报服务失败: %v", err)
	}
	if reportServer != nil {
		defer reportServer.Stop()
	}

	// 启动服务器
	go func() {
		logger.Infof("服务器启动在 %s", cfg.Server.Port)
		if err := engine.Run(cfg.Server.Port); err != nil {
			logger.Fatalf("服务器启动失败: %v", err)
		}
	}()

	// 启动节点健康检测服务
	healthService := service.NewNodeHealthService(db)
	healthService.Start()

	// 启动规则状态同步服务
	syncService := service.NewRuleSyncService(db)
	syncService.Start()
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	go service.NewGostRulesetService(db).Run(backgroundCtx)
	go nodeUpgrade.Run(backgroundCtx)

	// 启动自动备份服务
	backupService := service.NewBackupService(db)
	backupService.Start()

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("yagp 正在关闭...")

	// 停止相关的后台服务
	stopBackground()
	healthService.Stop()
	syncService.Stop()
	backupService.Stop()
}

func startReports(cfg config.ReportConfig, db *gorm.DB) (*grpc.Server, error) {
	if cfg.Listen == "" {
		return nil, nil
	}
	address, err := nodegrpc.ParsePanelAddress(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("report.address: %w", err)
	}
	if cfg.ProxyIP != "" && !address.Proxied() {
		return nil, fmt.Errorf("配置 report.proxy_ip 后，report.address 必须以 %s 结尾", nodegrpc.PanelProxyPath)
	}
	if cfg.ProxyIP == "" && address.Proxied() {
		return nil, fmt.Errorf("report.address 使用 %s 时必须配置 report.proxy_ip", nodegrpc.PanelProxyPath)
	}
	options, err := reportTransport(cfg)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(append(options, grpc.MaxRecvMsgSize(5<<20))...)
	nodegrpc.RegisterV1Server(server, handler.NewReportHandler(db, cfg.ProxyIP != ""))
	go func() {
		logger.Infof("gRPC 流量上报服务监听 %s", listener.Addr())
		if err := server.Serve(listener); err != nil {
			logger.Fatalf("流量上报服务失败: %v", err)
		}
	}()
	return server, nil
}

func reportTransport(cfg config.ReportConfig) ([]grpc.ServerOption, error) {
	if cfg.ProxyIP == "" {
		tlsCredentials, err := credentials.NewServerTLSFromFile(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		return []grpc.ServerOption{grpc.Creds(tlsCredentials)}, nil
	}

	proxyIP, err := netip.ParseAddr(cfg.ProxyIP)
	proxyIP = proxyIP.Unmap()
	if err != nil || proxyIP.Zone() != "" || !(proxyIP.IsGlobalUnicast() || proxyIP.IsLoopback()) {
		return nil, fmt.Errorf("report.proxy_ip 必须是代理的固定 IP 地址")
	}
	// The proxy terminates TLS and is the only peer allowed on this h2c listener.
	trusted := func(ctx context.Context) bool {
		if p, ok := peer.FromContext(ctx); ok {
			if addr, ok := p.Addr.(*net.TCPAddr); ok && addr.AddrPort().Addr().Unmap() == proxyIP {
				return true
			}
		}
		return false
	}
	return []grpc.ServerOption{
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			if !trusted(ctx) {
				return nil, status.Error(codes.PermissionDenied, "untrusted report proxy")
			}
			return next(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			if !trusted(stream.Context()) {
				return status.Error(codes.PermissionDenied, "untrusted report proxy")
			}
			return next(srv, stream)
		}),
	}, nil
}

// initDatabase 初始化数据库
func initDatabase(cfg *config.Config) (*gorm.DB, error) {
	// 配置 GORM 日志
	var gormLogLevel gormlogger.LogLevel
	switch cfg.Log.Level {
	case "debug":
		gormLogLevel = gormlogger.Info
	case "info":
		gormLogLevel = gormlogger.Warn
	default:
		gormLogLevel = gormlogger.Error
	}

	gormConfig := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormLogLevel),
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Database.Type)) {
	case "sqlite":
		return gorm.Open(sqlite.Open(cfg.Database.Path), gormConfig)
	case "mysql":
		if strings.TrimSpace(cfg.Database.DSN) == "" {
			return nil, fmt.Errorf("MySQL 需要配置 database.dsn 或 YAGP_DATABASE_DSN")
		}
		dsn, err := mysqldriver.ParseDSN(cfg.Database.DSN)
		if err != nil || dsn.DBName == "" {
			return nil, fmt.Errorf("MySQL 连接串无效或未指定数据库名")
		}
		dsn.ParseTime = true
		return gorm.Open(mysql.New(mysql.Config{DSNConfig: dsn}), gormConfig)
	default:
		return nil, fmt.Errorf("不支持的数据库类型 %q，请使用 sqlite 或 mysql", cfg.Database.Type)
	}
}

// autoMigrate 自动迁移数据库表结构
func autoMigrate(db *gorm.DB) error {
	// 1. 执行自动迁移（添加新字段）
	if err := db.AutoMigrate(
		&model.GostNode{},
		&model.GostRule{},
		&model.GostTunnel{},
		&model.GostAllowlist{},
		&model.GostRuleset{},
		&model.OperationLog{},
		&model.SystemConfig{},
	); err != nil {
		return err
	}

	return nil
}

// initSystemConfig 初始化系统配置
func initSystemConfig(db *gorm.DB) error {
	const upstreamURL = "https://github.com/code-gopher/gostPanel"

	var count int64
	if err := db.Model(&model.SystemConfig{}).Count(&count).Error; err != nil {
		return err
	}

	if count != 0 {
		return nil
	}

	sysConfig := &model.SystemConfig{
		SiteTitle:            "yagp",
		LogoURL:              "https://gost.run/images/gost.png",
		Copyright:            upstreamURL,
		LogRetentionDays:     7,
		LogLevel:             "info",
		AutoBackup:           false,
		BackupRetentionCount: 7,
	}
	if err := db.Create(sysConfig).Error; err != nil {
		return err
	}
	logger.Info("初始化默认系统配置完成")
	return nil
}
