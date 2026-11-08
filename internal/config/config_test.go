package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadDefaultsAndDatabaseEnvironment(t *testing.T) {
	t.Setenv("YAGP_DATABASE_TYPE", "")
	t.Setenv("YAGP_DATABASE_DSN", "")
	t.Chdir(t.TempDir())
	loaded, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if Get() != loaded || loaded.Server.Port != ":39100" || loaded.Server.Mode != "debug" ||
		loaded.Database.Type != "sqlite" || loaded.Database.Path != "./data/gost-panel.db" ||
		loaded.Authentik.AdminGroup != "yagp-admins" || !slices.Equal(loaded.Authentik.TrustedProxies, []string{"127.0.0.1", "::1"}) ||
		loaded.Log.Level != "info" || loaded.Log.Format != "json" || loaded.Log.Output != "./logs/app.log" {
		t.Fatalf("unexpected defaults: %+v", loaded)
	}
	const dsn = "user:password@tcp(database:3306)/panel"
	t.Setenv("YAGP_DATABASE_TYPE", "mysql")
	t.Setenv("YAGP_DATABASE_DSN", dsn)
	path := "settings.yaml"
	data := []byte("server:\n  port: ':5000'\n  mode: release\ndatabase:\n  type: sqlite\n  path: /data/panel.db\nauthentik:\n  trusted_proxies: [10.0.0.0/8]\n  admin_group: Operators\nreport:\n  listen: ':9000'\n  address: 'panel.example.test:9000'\nlog:\n  level: debug\n  format: console\n  output: stdout\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Database.Type != "mysql" || loaded.Database.DSN != dsn || loaded.Database.Path != "/data/panel.db" ||
		loaded.Server.Mode != "release" || loaded.Server.Port != ":5000" ||
		loaded.Authentik.AdminGroup != "Operators" || !slices.Equal(loaded.Authentik.TrustedProxies, []string{"10.0.0.0/8"}) ||
		loaded.Report.Address != "panel.example.test:9000" || loaded.Report.Listen != ":9000" || loaded.Log.Output != "stdout" {
		t.Fatalf("file values or environment overrides were lost: %+v", loaded)
	}
}

func TestLoadRejectsMissingMalformedAndWrongTypes(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name, yaml, want string
	}{
		{"missing", "", "读取配置文件失败"},
		{"malformed", "server: [broken", "读取配置文件失败"},
		{"wrong type", "server:\n  port: {nested: true}\n", "解析配置文件失败"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+".yaml")
			if test.yaml != "" {
				if err := os.WriteFile(path, []byte(test.yaml), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load = %v, want %q", err, test.want)
			}
		})
	}
}
