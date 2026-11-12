package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yagp/internal/config"
)

func TestServerRejectsInvalidStartup(t *testing.T) {
	const childConfig = "YAGP_TEST_INVALID_SERVER_CONFIG"
	if path := os.Getenv(childConfig); path != "" {
		flag.CommandLine = flag.NewFlagSet("yagp", flag.ExitOnError)
		os.Args = []string{"yagp", "-c", path}
		main()
		return
	}

	for _, fixture := range []struct {
		name, settings, dbType, setupSQL, want string
		migrate, occupyPort                    bool
	}{
		{name: "database engine", dbType: "unknown", want: "初始化数据库失败"},
		{name: "incompatible database schema", setupSQL: "CREATE VIEW nodes AS SELECT 1 AS id", want: "数据库迁移失败"},
		{
			name: "cannot seed settings", migrate: true, want: "初始化系统配置失败",
			setupSQL: `CREATE TRIGGER reject_settings BEFORE INSERT ON system_configs
				BEGIN SELECT RAISE(ABORT, 'fixture: settings storage unavailable'); END`,
		},
		{name: "unusable report listener", settings: "report:\n  listen: invalid-port\n", want: "启动流量上报服务失败"},
		{name: "malformed proxy address", settings: "authentik:\n  trusted_proxies: [invalid]\n", want: "反向代理配置无效"},
		{name: "unrestricted trusted proxies", settings: "authentik:\n  trusted_proxies: [0.0.0.0/0]\n", want: "Authentik 配置无效"},
		{name: "occupied web listener", occupyPort: true, want: "服务器启动失败"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "panel.db")
			if fixture.setupSQL != "" {
				prepareStartupDatabase(t, dbPath, fixture.migrate, fixture.setupSQL)
			}
			address := "127.0.0.1:0"
			if fixture.occupyPort {
				listener, err := net.Listen("tcp4", address)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				address = listener.Addr().String()
			}
			contents := fmt.Sprintf("host: 127.0.0.1\nserver:\n  port: %q\n  mode: release\ndatabase:\n  path: %q\n%s", address, dbPath, fixture.settings)
			path := filepath.Join(dir, "panel.yaml")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerRejectsInvalidStartup$")
			child.Dir = dir
			child.Env = append(os.Environ(), childConfig+"="+path, "YAGP_HOST=", "YAGP_DATABASE_TYPE="+fixture.dbType, "YAGP_DATABASE_DSN=")
			if coverage := flag.Lookup("test.gocoverdir"); coverage != nil && coverage.Value.String() != "" {
				child.Env = append(child.Env, "GOCOVERDIR="+coverage.Value.String())
			}
			output, err := child.CombinedOutput()
			if err == nil || ctx.Err() != nil || !bytes.Contains(output, []byte(fixture.want)) {
				t.Fatalf("invalid configuration startup = %v\n%s", err, output)
			}
		})
	}
}

func prepareStartupDatabase(t *testing.T, path string, migrate bool, fixture string) {
	t.Helper()
	db, err := initDatabase(&config.Config{Database: config.DatabaseConfig{Type: "sqlite", Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if migrate {
		if err := autoMigrate(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(fixture).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReportTokenKeyStorage(t *testing.T) {
	t.Run("private durable key", func(t *testing.T) {
		t.Chdir(t.TempDir())
		first, err := loadNodeTokenKey()
		if err != nil || len(first) != 32 {
			t.Fatalf("first token key length = %d, %v", len(first), err)
		}
		second, err := loadNodeTokenKey()
		if err != nil || !bytes.Equal(second, first) {
			t.Fatalf("loading the key again changed it: %v", err)
		}
		info, err := os.Stat(nodeTokenKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("token key permissions = %o, want 0600", info.Mode().Perm())
		}
	})

	for _, kind := range []string{"invalid key", "key is a directory", "data symlink has no target"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			dir := filepath.Dir(nodeTokenKeyPath)
			want := "加载节点凭据密钥"
			switch kind {
			case "invalid key":
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(nodeTokenKeyPath, []byte("damaged"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "node token key must be 32 bytes"
			case "key is a directory":
				if err := os.MkdirAll(nodeTokenKeyPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "data symlink has no target":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dir); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symbolic links unavailable: %v", err)
					}
					t.Fatal(err)
				}
			}
			if server, _, err := startReports(config.ReportConfig{Listen: "127.0.0.1:0"}, "127.0.0.1", testSQLite(t)); server != nil || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("startReports = %v, %v; want %q", server, err, want)
			}
			if kind == "invalid key" {
				if contents, err := os.ReadFile(nodeTokenKeyPath); err != nil || string(contents) != "damaged" {
					t.Fatalf("startup replaced the invalid token key: %v", err)
				}
			}
		})
	}
}
