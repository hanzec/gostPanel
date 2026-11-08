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
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	if server, pin, err := startReports(config.ReportConfig{}, "", db); err != nil || server != nil || pin != "" {
		t.Fatalf("disabled reports = %v, %v", server, err)
	}
	cfg := config.ReportConfig{Listen: "127.0.0.1:0", DirectTLS: true}
	if _, _, err := startReports(cfg, "127.0.0.1:9000", db); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("invalid report host = %v", err)
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
	server, pin, err := startReports(cfg, "127.0.0.1", db)
	if err != nil || pin != "" {
		t.Fatalf("configured TLS certificate = pin %q, %v", pin, err)
	}
	defer server.Stop()
	info, ok := server.GetServiceInfo()[strings.TrimPrefix(nodegrpc.PanelRPCPath, "/")]
	if !ok || len(info.Methods) != 16 {
		t.Fatalf("registered report API = %+v", info)
	}
	trust, err := nodegrpc.ClientTLS(cfg.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	conn := testPanelClient(t, "127.0.0.1", cfg.Listen, trust)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := nodegrpc.NewV1Client(conn)
	if _, err := client.GetNftTemplate(ctx, &nodegrpc.NftTemplateRequest{NodeId: 1}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated report RPC = %v", err)
	}
	if _, err := client.Execute(ctx, &nodegrpc.CommandRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("node control RPC on panel listener = %v", err)
	}
	if _, err := client.CheckListener(ctx, &nodegrpc.ListenerCheckRequest{Protocol: "tcp", Port: 9718}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("listener probe should run only on the node control stream: %v", err)
	}
	cfg.Listen = "invalid listen address"
	if _, _, err := startReports(cfg, "127.0.0.1", db); err == nil {
		t.Fatal("invalid bind address accepted")
	}
	cfg.KeyFile = ""
	if _, _, err := startReports(cfg, "127.0.0.1", db); err == nil || !strings.Contains(err.Error(), "report.key_file") {
		t.Fatalf("incomplete explicit TLS configuration = %v", err)
	}
}

func testPanelClient(t *testing.T, host, listen string, transport credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	panel, err := nodegrpc.ParsePanelHost(host)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := panel.NewClientConn(grpc.WithTransportCredentials(transport), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listen)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestRegistrationWithGeneratedTLSOrInternalProxy(t *testing.T) {
	for _, proxied := range []bool{false, true} {
		name := "generated TLS"
		if proxied {
			name = "internal proxy without a fixed IP"
		}
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			db := testSQLite(t)
			if err := db.AutoMigrate(&model.GostNode{}, &model.OperationLog{}); err != nil {
				t.Fatal(err)
			}
			node := model.GostNode{Name: "enrollment"}
			if err := db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.ReportConfig{Listen: listener.Addr().String(), DirectTLS: !proxied}
			_ = listener.Close()
			const host = "127.0.0.1"
			server, pin, err := startReports(cfg, host, db)
			if err != nil || (proxied && pin != "") || (!proxied && len(pin) != 64) {
				t.Fatalf("start reports = pin %q, %v", pin, err)
			}
			defer server.Stop()
			issued, err := service.NewNodeService(db, pin).IssueRegistrationToken(t.Context(), node.ID, "admin", "127.0.0.1", "test")
			if err != nil {
				t.Fatal(err)
			}
			_, enrollmentPin, err := nodegrpc.ParseRegistrationToken(issued.Token)
			if err != nil || enrollmentPin != pin {
				t.Fatalf("enrollment certificate pin = %q, %v", enrollmentPin, err)
			}
			transport := insecure.NewCredentials()
			if !proxied {
				panel, err := nodegrpc.ParsePanelHost(host)
				if err != nil {
					t.Fatal(err)
				}
				transport, err = panel.ClientTLS("", enrollmentPin)
				if err != nil {
					t.Fatal(err)
				}
			}
			conn := testPanelClient(t, host, cfg.Listen, transport)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			client := nodegrpc.NewV1Client(conn)
			stream, err := client.Connect(ctx)
			if err == nil {
				_, err = stream.Recv()
			}
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("unauthenticated connect = %v", err)
			}
			request := &nodegrpc.RegisterNodeRequest{Token: issued.Token}
			if proxied {
				ctx = metadata.AppendToOutgoingContext(ctx, "x-real-ip", "192.0.2.20")
				if _, err := client.RegisterNode(ctx, request); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("unauthorized forwarded IP accepted = %v", err)
				}
			}
			request.Ipv4 = "192.0.2.21"
			registered, err := client.RegisterNode(ctx, request)
			if err != nil || registered.GetNodeId() != uint64(node.ID) {
				t.Fatalf("register node = %v", err)
			}
			if err := db.First(&node, node.ID).Error; err != nil || node.IPv4 != request.Ipv4 {
				t.Fatalf("stored enrollment address = %q, %v", node.IPv4, err)
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
