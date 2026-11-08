package handler

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yagp/internal/config"
	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const configuredSystemConfigPayload = `{
	"email":{"host":"mail.example","port":587,"username":"mail-user","password":"mail-password","fromEmail":"admin@example.org"},
	"config":{"siteTitle":"Forwarding panel","logoUrl":"https://example.org/logo.svg","copyright":"Example"},
	"log":{"retentionDays":30,"level":"debug"},
	"backup":{"autoBackup":true,"retentionCount":1}
}`

const clearSystemConfigPayload = `{
	"email":{"host":"","port":0,"username":"","password":"","fromEmail":""},
	"config":{"siteTitle":"","logoUrl":"","copyright":""},
	"log":{"retentionDays":0,"level":""},
	"backup":{"autoBackup":false,"retentionCount":0}
}`

func TestSystemHTTPSettingsAndRulesets(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	admin := handlerData[map[string]string](t, f.request(t, "GET", "/auth/info", "", 200))
	if admin["username"] != "test-admin" || admin["role"] != "admin" {
		t.Fatalf("current admin: %v", admin)
	}

	f.request(t, "PUT", "/system/config", "{", 400)
	f.request(t, "PUT", "/system/config", configuredSystemConfigPayload, 200)
	full := handlerData[dto.SystemConfigResp](t, f.request(t, "GET", "/system/config", "", 200))
	if full.Config.SiteTitle != "Forwarding panel" || full.Email.Port != 587 || full.Log.RetentionDays != 30 ||
		!full.Backup.Supported || !full.Backup.AutoBackup || full.Backup.RetentionCount != 1 {
		t.Fatalf("updated system config: %+v", full)
	}
	reply := f.request(t, "GET", "/system/public-config", "", 200)
	public := handlerData[dto.PublicSystemConfigResp](t, reply)
	if public.SiteTitle != "Forwarding panel" || public.Host != "panel.example" ||
		strings.Contains(string(reply.Data), "mail-password") {
		t.Fatalf("public config: %s", reply.Data)
	}

	node := model.GostNode{Name: "relay", Port: 39110, IPv4: "198.51.100.10", IPv6: "2001:db8::5"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "GET", "/system/gost-allowlist", "", 200)
	f.request(t, "PUT", "/system/gost-allowlist", `{"node_ids":[0]}`, 400)
	f.request(t, "PUT", "/system/gost-allowlist", `{"node_ids":[999]}`, 400)
	allowlist := handlerData[model.GostAllowlist](t, f.request(t, "PUT", "/system/gost-allowlist", fmt.Sprintf(`{"node_ids":[%d,%d]}`, node.ID, node.ID), 200))
	if !slices.Equal(allowlist.NodeIDs, []uint{node.ID}) || !slices.Equal(allowlist.Addresses, []string{"198.51.100.10", "2001:db8::5"}) {
		t.Fatalf("resolved allowlist: %+v", allowlist)
	}
	if err := f.db.Model(&node).Update("ipv4", "198.51.100.11").Error; err != nil {
		t.Fatal(err)
	}
	updated := handlerData[model.GostAllowlist](t, f.request(t, "GET", "/system/gost-allowlist", "", 200))
	if !slices.Contains(updated.Addresses, "198.51.100.11") || slices.Contains(updated.Addresses, "198.51.100.10") {
		t.Fatalf("allowlist contains stale node addresses: %+v", updated)
	}

	f.request(t, "PUT", "/system/gost-ruleset", "{", 400)
	f.request(t, "PUT", "/system/gost-ruleset", fmt.Sprintf(`{"url":"","node_ids":[%d]}`, node.ID), 400)
	f.request(t, "PUT", "/system/gost-ruleset", `{"url":"http://example.org/rules.yaml","node_ids":[]}`, 400)
	f.request(t, "PUT", "/system/gost-ruleset", `{"url":"","node_ids":[]}`, 200)
	if rules := handlerData[model.GostRuleset](t, f.request(t, "GET", "/system/gost-ruleset", "", 200)); rules.UpdatedAt.IsZero() || rules.URL != "" || len(rules.NodeIDs) != 0 {
		t.Fatalf("cleared ruleset: %+v", rules)
	}
}

func TestSystemConfigUpdateRequiresExplicitFields(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	const path = "/system/config"
	f.request(t, "PUT", path, configuredSystemConfigPayload, 200)
	before := handlerData[dto.SystemConfigResp](t, f.request(t, "GET", path, "", 200))

	for _, test := range []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"empty object", `{}`},
		{"null body", `null`},
		{"omitted section", strings.Replace(configuredSystemConfigPayload, `"log":{"retentionDays":30,"level":"debug"},`, "", 1)},
		{"null section", strings.Replace(configuredSystemConfigPayload, `"log":{"retentionDays":30,"level":"debug"}`, `"log":null`, 1)},
		{"empty sections", `{"email":{},"config":{},"log":{},"backup":{}}`},
		{"omitted email password", strings.Replace(configuredSystemConfigPayload, `"password":"mail-password",`, "", 1)},
		{"omitted site title", strings.Replace(configuredSystemConfigPayload, `"siteTitle":"Forwarding panel",`, "", 1)},
		{"null log retention", strings.Replace(configuredSystemConfigPayload, `"retentionDays":30`, `"retentionDays":null`, 1)},
		{"omitted backup switch", strings.Replace(configuredSystemConfigPayload, `"autoBackup":true,`, "", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.request(t, "PUT", path, test.body, 400)
			after := handlerData[dto.SystemConfigResp](t, f.request(t, "GET", path, "", 200))
			if after != before {
				t.Fatal("rejected update changed system config")
			}
		})
	}

	f.request(t, "PUT", path, clearSystemConfigPayload, 200)
	cleared := handlerData[dto.SystemConfigResp](t, f.request(t, "GET", path, "", 200))
	if cleared != (dto.SystemConfigResp{Backup: dto.BackupConfigResp{Supported: true}}) {
		t.Fatal("explicit empty values did not clear system config")
	}
}

func TestSystemHTTPBackup(t *testing.T) {
	t.Chdir(t.TempDir())
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	if err := f.db.Create(&model.SystemConfig{SiteTitle: "Backup test", BackupRetentionCount: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("backups", 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join("backups", "gost_panel_20000101_000000.db")
	if err := os.WriteFile(stale, []byte("old backup"), 0600); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/system/backup", "", 200)

	// Old-file cleanup is asynchronous. Wait until it leaves the backup we can restore.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := os.Stat(stale)
		if os.IsNotExist(err) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("backup retention did not remove the old backup: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	files, err := filepath.Glob(filepath.Join("backups", "gost_panel_*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("backup files: %v, %v", files, err)
	}
	backup, err := gorm.Open(sqlite.Open(files[0]), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	backupSQL, err := backup.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backupSQL.Close() })
	var site model.SystemConfig
	if err := backup.First(&site).Error; err != nil || site.SiteTitle != "Backup test" {
		t.Fatalf("restore from backup: %+v, %v", site, err)
	}
	config.Get().Database.Path = ""
	f.request(t, "POST", "/system/backup", "", 500)

	// The MySQL backup guard relies on the configured dialect, so it needs no live MySQL server.
	raw, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	mysqlDB, err := gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	mysqlFixture := &handlerHTTPFixture{engine: gin.New()}
	mysqlFixture.engine.POST("/system/backup", NewSystemConfigHandler(nil, service.NewBackupService(mysqlDB)).Backup)
	mysqlFixture.request(t, "POST", "/system/backup", "", 501)
}

func TestSystemHTTPEmail(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	f.request(t, "POST", "/system/email/test", "{", 400)
	f.request(t, "POST", "/system/email/test", "{}", 500)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() { result <- receiveTestEmail(listener) }()
	port := listener.Addr().(*net.TCPAddr).Port
	body := fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"username":"user","password":"password","fromEmail":"admin@example.org","toEmail":"test@example.org"}`, port)
	f.request(t, "POST", "/system/email/test", body, 200)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func receiveTestEmail(listener net.Listener) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	reader := textproto.NewReader(bufio.NewReader(conn))
	writer := textproto.NewWriter(bufio.NewWriter(conn))
	if err := writer.PrintfLine("220 local SMTP"); err != nil {
		return err
	}
	exchanges := []struct {
		command string
		reply   string
	}{
		{"EHLO ", "250-localhost\r\n250 AUTH PLAIN"},
		{"AUTH PLAIN ", "235 authenticated"},
		{"MAIL FROM:<admin@example.org>", "250 sender"},
		{"RCPT TO:<test@example.org>", "250 recipient"},
		{"DATA", "354 send mail"},
	}
	for _, exchange := range exchanges {
		line, err := reader.ReadLine()
		if err != nil || !strings.HasPrefix(line, exchange.command) {
			return fmt.Errorf("SMTP command %q, want %q: %v", line, exchange.command, err)
		}
		if err := writer.PrintfLine("%s", exchange.reply); err != nil {
			return err
		}
	}
	data, err := reader.ReadDotBytes()
	if err != nil || !strings.Contains(string(data), "Subject: yagp 测试邮件") || !strings.Contains(string(data), "To: test@example.org") {
		return fmt.Errorf("test email %q: %v", data, err)
	}
	if err := writer.PrintfLine("250 queued"); err != nil {
		return err
	}
	if line, err := reader.ReadLine(); err != nil || line != "QUIT" {
		return fmt.Errorf("SMTP termination %q: %v", line, err)
	}
	return writer.PrintfLine("221 goodbye")
}
