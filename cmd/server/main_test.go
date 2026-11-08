package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagp/internal/config"
	"yagp/internal/model"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func testSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Database: config.DatabaseConfig{Type: " SQLITE ", Path: filepath.Join(t.TempDir(), "test.db")}}
	db, err := initDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestServerMigratesAndSeedsSettingsOnce(t *testing.T) {
	db := testSQLite(t)
	if err := initSystemConfig(db); err == nil {
		t.Fatal("settings initialization before migration unexpectedly succeeded")
	}
	if err := autoMigrate(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{
		&model.GostNode{}, &model.GostRule{}, &model.GostTunnel{}, &model.GostAllowlist{},
		&model.GostRuleset{}, &model.OperationLog{}, &model.SystemConfig{},
	} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("missing table for %T", table)
		}
	}
	if err := initSystemConfig(db); err != nil {
		t.Fatal(err)
	}
	var settings model.SystemConfig
	if err := db.First(&settings, 1).Error; err != nil || settings.SiteTitle != "yagp" || settings.BackupRetentionCount != 7 {
		t.Fatalf("seeded settings = %+v, %v", settings, err)
	}
	if err := db.Model(&settings).Update("site_title", "Custom").Error; err != nil {
		t.Fatal(err)
	}
	if err := initSystemConfig(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&settings, 1).Error; err != nil || settings.SiteTitle != "Custom" {
		t.Fatalf("existing settings changed: %+v, %v", settings, err)
	}
	sqlDB, _ := db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := autoMigrate(db); err == nil {
		t.Fatal("migration against closed DB succeeded")
	}
}

func TestServerDatabaseConfigurationErrors(t *testing.T) {
	for _, test := range []struct {
		name, dsn, level, want string
	}{
		{"postgres", "", "debug", "不支持的数据库类型"},
		{"mysql", "", "info", "需要配置"},
		{"mysql", "user@tcp(localhost:3306)/", "warn", "未指定数据库名"},
		{"mysql", "not a mysql dsn", "warn", "连接串无效"},
	} {
		cfg := &config.Config{Database: config.DatabaseConfig{Type: test.name, DSN: test.dsn}, Log: config.LogConfig{Level: test.level}}
		if _, err := initDatabase(cfg); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("init database %q/%q = %v, want %q", test.name, test.dsn, err, test.want)
		}
	}
}

func TestStartReportsRequiresTLSAndRegistersPanelAPI(t *testing.T) {
	db := testSQLite(t)
	if err := db.AutoMigrate(&model.GostNode{}); err != nil {
		t.Fatal(err)
	}
	if server, err := startReports(config.ReportConfig{}, db); err != nil || server != nil {
		t.Fatalf("disabled reports = %v, %v", server, err)
	}
	cfg := config.ReportConfig{Listen: "127.0.0.1:0", Address: ":9000"}
	if _, err := startReports(cfg, db); err == nil || !strings.Contains(err.Error(), "report.address") {
		t.Fatalf("invalid report address = %v", err)
	}
	cfg.Address = "panel.example.test:9000"
	if _, err := startReports(cfg, db); err == nil {
		t.Fatal("reports started without TLS files")
	}
	https := httptest.NewTLSServer(nil)
	cert := https.TLS.Certificates[0]
	https.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.CertFile = filepath.Join(dir, "panel.crt")
	cfg.KeyFile = filepath.Join(dir, "panel.key")
	for path, block := range map[string]*pem.Block{
		cfg.CertFile: {Type: "CERTIFICATE", Bytes: cert.Certificate[0]},
		cfg.KeyFile:  {Type: "PRIVATE KEY", Bytes: key},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = listener.Addr().String()
	_ = listener.Close()
	server, err := startReports(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	info, ok := server.GetServiceInfo()[nodegrpc.V1_ServiceDesc.ServiceName]
	if !ok || len(info.Methods) != 4 {
		t.Fatalf("registered report API = %+v", info)
	}
	trust, err := nodegrpc.ClientTLS(cfg.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(cfg.Listen, grpc.WithTransportCredentials(trust))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := nodegrpc.NewV1Client(conn).GetNftTemplate(ctx, &nodegrpc.NftTemplateRequest{NodeId: 1}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated report RPC = %v", err)
	}
	cfg.Listen = "invalid listen address"
	if _, err := startReports(cfg, db); err == nil {
		t.Fatal("invalid bind address accepted")
	}
}

func TestStartReportsAllowsOnlyTheConfiguredProxy(t *testing.T) {
	db := testSQLite(t)
	cfg := config.ReportConfig{Listen: "127.0.0.1:0", Address: "panel.example.test:443"}
	cfg.ProxyIP = "127.0.0.1"
	if _, err := startReports(cfg, db); err == nil || !strings.Contains(err.Error(), "/api/v1/grpc") {
		t.Fatalf("proxied reports without a path = %v", err)
	}
	cfg.Address += "/api/v1/grpc"
	cfg.ProxyIP = ""
	if _, err := startReports(cfg, db); err == nil || !strings.Contains(err.Error(), "report.proxy_ip") {
		t.Fatalf("direct reports with a proxy path = %v", err)
	}
	cfg.ProxyIP = "127.0.0.0/8"
	if _, err := startReports(cfg, db); err == nil || !strings.Contains(err.Error(), "report.proxy_ip") {
		t.Fatalf("proxy IP with a CIDR = %v", err)
	}

	for _, tt := range []struct {
		proxyIP string
		want    codes.Code
	}{
		{"127.0.0.1", codes.Unauthenticated},
		{"127.0.0.2", codes.PermissionDenied},
	} {
		t.Run(tt.proxyIP, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Listen = listener.Addr().String()
			_ = listener.Close()
			cfg.ProxyIP = tt.proxyIP
			server, err := startReports(cfg, db)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Stop()

			conn, err := grpc.NewClient(cfg.Listen, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = nodegrpc.NewV1Client(conn).RegisterNode(ctx, &nodegrpc.RegisterNodeRequest{})
			if status.Code(err) != tt.want {
				t.Fatalf("registration from 127.0.0.1 = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestServerCLIHandlesVersionAndInvalidConfig(t *testing.T) {
	args, flags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = args, flags })
	flag.CommandLine = flag.NewFlagSet("yagp", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"yagp", "--version"}
	main()
	flag.CommandLine = flag.NewFlagSet("yagp", flag.ContinueOnError)
	os.Args = []string{"yagp", "-c", filepath.Join(t.TempDir(), "missing.yaml")}
	defer func() {
		if failure, ok := recover().(string); !ok || !strings.Contains(failure, "加载配置失败") {
			t.Errorf("expected a configuration panic, got %v", failure)
		}
	}()
	main()
}
