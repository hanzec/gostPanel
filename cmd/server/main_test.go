package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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

	"google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"
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

func TestStartReportsUsesConfiguredTLS(t *testing.T) {
	t.Chdir(t.TempDir())
	db := testSQLite(t)
	if err := autoMigrate(db); err != nil {
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
	server, pin, err := startReports(cfg, "127.0.0.1", db)
	if err != nil || pin != "" {
		t.Fatalf("configured TLS certificate = pin %q, %v", pin, err)
	}
	t.Cleanup(func() { _ = server.Close() })
	panel, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	trust, err := panel.TLSConfig(cfg.CertFile, "")
	if err != nil {
		t.Fatal(err)
	}
	client := testPanelHTTPClient(t, server.Addr().String(), trust)
	for _, path := range []string{"/api/grpc/GetNftTemplate", "/api/node/v2/nft-template"} {
		err := nodegrpc.PostProtobuf(t.Context(), client, panel.HTTPURL(path), &emptypb.Empty{}, &emptypb.Empty{})
		assertReportHTTPStatus(t, err, http.StatusNotFound)
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

func testPanelHTTPClient(t *testing.T, listen string, config *tls.Config) *http.Client {
	t.Helper()
	transport := &http.Transport{
		TLSClientConfig: config,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", listen)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func assertReportHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	var status *nodegrpc.HTTPStatusError
	if !errors.As(err, &status) || status.StatusCode != want {
		t.Fatalf("report error = %v, want HTTP %d", err, want)
	}
}

func TestNodeSessionWithGeneratedTLSOrInternalProxy(t *testing.T) {
	for _, proxied := range []bool{false, true} {
		name := "generated TLS"
		if proxied {
			name = "internal proxy without a fixed IP"
		}
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			db := testSQLite(t)
			if err := autoMigrate(db); err != nil {
				t.Fatal(err)
			}
			node := model.GostNode{Name: "enrollment"}
			if err := db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			cfg := config.ReportConfig{Listen: "127.0.0.1:0", DirectTLS: !proxied}
			const host = "127.0.0.1"
			server, pin, err := startReports(cfg, host, db)
			if err != nil || (proxied && pin != "") || (!proxied && len(pin) != 64) {
				t.Fatalf("start reports = pin %q, %v", pin, err)
			}
			t.Cleanup(func() { _ = server.Close() })
			nodes := service.NewNodeService(db, pin)
			issued, err := nodes.IssueRegistrationToken(t.Context(), node.ID, "admin", "127.0.0.1", "test")
			if err != nil {
				t.Fatal(err)
			}
			_, enrollmentPin, err := nodegrpc.ParseRegistrationToken(issued.Token)
			if err != nil || enrollmentPin != pin {
				t.Fatalf("enrollment certificate pin = %q, %v", enrollmentPin, err)
			}
			panel, err := nodegrpc.ParsePanelHost(host)
			if err != nil {
				t.Fatal(err)
			}
			address := server.Addr().String()
			var trust *tls.Config
			if proxied {
				forward := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: address})
				proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.Header.Set("X-Real-IP", "192.0.2.20")
					forward.ServeHTTP(w, r)
				}))
				t.Cleanup(proxy.Close)
				address = proxy.Listener.Addr().String()
				trust = proxy.Client().Transport.(*http.Transport).TLSClientConfig
			} else {
				trust, err = panel.TLSConfig("", enrollmentPin)
				if err != nil {
					t.Fatal(err)
				}
				wrongTrust, err := panel.TLSConfig("", strings.Repeat("0", 64))
				if err != nil {
					t.Fatal(err)
				}
				wrongClient := testPanelHTTPClient(t, address, wrongTrust)
				err = nodegrpc.PostProtobuf(t.Context(), wrongClient, panel.HTTPURL(nodegrpc.RegisterPath), &nodegrpc.RegisterNodeRequest{}, &nodegrpc.RegisterNodeReply{})
				if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
					t.Fatalf("mismatched TLS pin = %v", err)
				}
			}
			client := testPanelHTTPClient(t, address, trust)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err = panel.DialSession(ctx, client, uint64(node.ID), "invalid")
			assertReportHTTPStatus(t, err, http.StatusUnauthorized)
			request := &nodegrpc.RegisterNodeRequest{Token: issued.Token}
			registered := &nodegrpc.RegisterNodeReply{}
			registerURL := panel.HTTPURL(nodegrpc.RegisterPath)
			if proxied {
				err := nodegrpc.PostProtobuf(ctx, client, registerURL, request, registered)
				assertReportHTTPStatus(t, err, http.StatusBadRequest)
			}
			request.Ipv4 = "192.0.2.21"
			if err := nodegrpc.PostProtobuf(ctx, client, registerURL, request, registered); err != nil {
				t.Fatal(err)
			}
			access := registered.GetCredentials().GetAccessToken()
			if registered.GetNodeId() != uint64(node.ID) || access == "" || registered.GetCredentials().GetRefreshToken() == "" {
				t.Fatal("registration did not return the node identity and credentials")
			}
			if err := db.First(&node, node.ID).Error; err != nil || node.IPv4 != request.Ipv4 {
				t.Fatalf("stored enrollment address = %q, %v", node.IPv4, err)
			}
			stream, err := panel.DialSession(ctx, client, uint64(node.ID), access)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			session := nodegrpc.NewSession(stream, nil)
			go func() { _ = session.Run() }()
			nodeClient := nodegrpc.NewV1Client(session)
			configuration, err := nodeClient.GetNodeConfig(ctx, &emptypb.Empty{})
			if err != nil || !strings.Contains(configuration.GetNftTemplate(), nodegrpc.NftTableName(uint64(node.ID))) {
				t.Fatalf("node configuration over WebSocket = %v, %v", configuration, err)
			}
			var settings struct {
				NodeID uint `yaml:"node_id"`
			}
			if err := yaml.Unmarshal(configuration.GetConfigYaml(), &settings); err != nil || settings.NodeID != node.ID {
				t.Fatalf("node ID in configuration = %d, %v", settings.NodeID, err)
			}
			refreshed := &nodegrpc.NodeCredentials{}
			refresh := &nodegrpc.RefreshNodeRequest{
				NodeId: uint64(node.ID), RefreshToken: registered.GetCredentials().GetRefreshToken(), RequestId: "refresh-test",
			}
			if err := nodegrpc.PostProtobuf(ctx, client, panel.HTTPURL(nodegrpc.RefreshPath), refresh, refreshed); err != nil {
				t.Fatal(err)
			}
			if refreshed.GetAccessToken() == "" || refreshed.GetAccessToken() == access || refreshed.GetRefreshToken() == refresh.GetRefreshToken() {
				t.Fatal("refresh did not rotate the node credentials")
			}
			if _, err := nodeClient.GetNodeConfig(ctx, &emptypb.Empty{}); err != nil {
				t.Fatalf("refresh disconnected the node: %v", err)
			}
			issued, err = nodes.IssueRegistrationToken(ctx, node.ID, "admin", "127.0.0.1", "test")
			if err != nil {
				t.Fatal(err)
			}
			request.Token = issued.Token
			replacement := &nodegrpc.RegisterNodeReply{}
			if err := nodegrpc.PostProtobuf(ctx, client, registerURL, request, replacement); err != nil {
				t.Fatal(err)
			}
			if replacement.GetUsername() != registered.GetUsername() || replacement.GetPassword() != registered.GetPassword() {
				t.Fatal("re-enrollment replaced the local Gost credentials")
			}
			select {
			case <-session.Done():
			case <-ctx.Done():
				t.Fatal("re-enrollment left the previous session connected")
			}
			_, err = panel.DialSession(ctx, client, uint64(node.ID), refreshed.GetAccessToken())
			assertReportHTTPStatus(t, err, http.StatusUnauthorized)
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
