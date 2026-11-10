package service

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSystemSettingsRoundTripAndPublicProjection(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "host: panel.test\nreport:\n  listen: ':39101'\n")
	settings := NewSystemConfigService(repository.NewSystemConfigRepository(db))
	defaults, err := settings.GetConfig()
	requireServiceError(t, err, nil)
	if defaults.Config.SiteTitle != "yagp" || defaults.Log.Level != "info" || defaults.Log.RetentionDays != 7 {
		t.Fatalf("default system settings = %+v", defaults)
	}
	request := &dto.UpdateSystemConfigReq{
		Email:  dto.EmailConfigReq{Host: "mail.test", Port: 587, Username: "account", Password: "test-password", FromEmail: "sender@example.test"},
		Config: dto.PanelSettingReq{SiteTitle: "Edge operations", LogoURL: "/logo.svg", Copyright: "2026"},
		Log:    dto.LogConfigReq{RetentionDays: 21, Level: "warn"},
		Backup: dto.BackupConfigReq{AutoBackup: true, RetentionCount: 3},
	}
	requireServiceError(t, settings.UpdateConfig(request), nil)
	updated, err := settings.GetConfig()
	requireServiceError(t, err, nil)
	want := dto.SystemConfigResp{
		Email:  dto.EmailConfigResp{Host: "mail.test", Port: 587, Username: "account", Password: "test-password", FromEmail: "sender@example.test"},
		Config: dto.PanelSettingResp(request.Config),
		Log:    dto.LogConfigResp(request.Log),
		Backup: dto.BackupConfigResp{AutoBackup: true, RetentionCount: 3},
	}
	if *updated != want {
		t.Fatal("saved system settings do not match the request")
	}
	public, err := settings.GetPublicConfig()
	requireServiceError(t, err, nil)
	if *public != (dto.PublicSystemConfigResp{SiteTitle: want.Config.SiteTitle, LogoURL: want.Config.LogoURL, Copyright: want.Config.Copyright, Host: "panel.test"}) {
		t.Fatalf("public settings = %+v", public)
	}
	loadServiceConfig(t, "host: panel.test\n")
	public, err = settings.GetPublicConfig()
	requireServiceError(t, err, nil)
	if public.Host != "" {
		t.Fatal("advertised a registration host with reporting disabled")
	}

	requireServiceError(t, db.Migrator().DropTable(&model.SystemConfig{}), nil)
	_, readErr := settings.GetConfig()
	_, publicErr := settings.GetPublicConfig()
	updateErr := settings.UpdateConfig(request)
	for _, err := range []error{readErr, publicErr, updateErr} {
		if err == nil || !strings.Contains(err.Error(), "no such table") {
			t.Fatalf("settings database error = %v", err)
		}
	}
}

func TestBarkSettingsRejectInvalidAndFailedUpdates(t *testing.T) {
	db := newServiceTestDB(t)
	settings := NewSystemConfigService(repository.NewSystemConfigRepository(db))
	req := dto.UpdateSystemConfigReq{
		Bark:   &dto.BarkConfig{ServerURL: " https://bark.example.test/notify ", DeviceKey: " test-key "},
		Config: dto.PanelSettingReq{SiteTitle: "Existing panel"},
	}
	requireServiceError(t, settings.UpdateConfig(&req), nil)
	before, err := settings.GetConfig()
	requireServiceError(t, err, nil)
	if want := (dto.BarkConfig{ServerURL: "https://bark.example.test/notify", DeviceKey: "test-key"}); before.Bark != want {
		t.Fatalf("Bark settings were not normalized: %+v", before.Bark)
	}

	req.Bark = &dto.BarkConfig{ServerURL: "https://bark.example.test/other"}
	req.Config.SiteTitle = "Rejected panel"
	requireServiceError(t, settings.UpdateConfig(&req), bizerrors.ErrBarkConfigIncomplete)
	after, err := settings.GetConfig()
	requireServiceError(t, err, nil)
	if *after != *before {
		t.Fatal("invalid Bark settings changed the saved configuration")
	}

	requireServiceError(t, db.Exec(`CREATE TRIGGER reject_bark_settings
		BEFORE UPDATE ON system_configs
		BEGIN SELECT RAISE(ABORT, 'settings write failed'); END`).Error, nil)
	req.Bark = &dto.BarkConfig{}
	requireServiceError(t, settings.UpdateConfig(&req), bizerrors.ErrOperationFailed)
	after, err = settings.GetConfig()
	requireServiceError(t, err, nil)
	if *after != *before {
		t.Fatal("failed Bark write changed the saved configuration")
	}
}

func TestBackupCreatesReadableSnapshotAndAppliesRetention(t *testing.T) {
	t.Chdir(t.TempDir())
	db := newServiceTestDB(t)
	loadServiceConfig(t, "database:\n  path: live.db\n")
	saveServiceFixtures(t, db, &model.GostNode{Name: "saved node", Port: 39000})
	repo := repository.NewSystemConfigRepository(db)
	settings, err := repo.Get()
	requireServiceError(t, err, nil)
	settings.AutoBackup, settings.BackupRetentionCount = true, 1
	requireServiceError(t, repo.Update(settings), nil)
	requireServiceError(t, os.Mkdir("backups", 0o700), nil)
	old := filepath.Join("backups", "gost_panel_19990101_000000.db")
	other := filepath.Join("backups", "manual.db")
	for _, path := range []string{old, other} {
		requireServiceError(t, os.WriteFile(path, []byte("old file"), 0o600), nil)
	}
	synctest.Test(t, func(t *testing.T) {
		backup := NewBackupService(db)
		if !backup.Supported() {
			t.Fatal("SQLite backups should be supported")
		}
		backup.Start()
		defer backup.Stop()
		synctest.Wait()
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Fatalf("retention left an older backup: %v", err)
		}
		files, err := filepath.Glob(filepath.Join("backups", "gost_panel_*.db"))
		requireServiceError(t, err, nil)
		if len(files) != 1 {
			t.Fatalf("backup files after retention = %v", files)
		}
		snapshot, err := gorm.Open(sqlite.Open(files[0]), &gorm.Config{})
		requireServiceError(t, err, nil)
		snapshotDB, err := snapshot.DB()
		requireServiceError(t, err, nil)
		defer snapshotDB.Close()
		var saved model.GostNode
		requireServiceError(t, snapshot.First(&saved).Error, nil)
		if saved.Name != "saved node" {
			t.Fatalf("snapshot node name = %q", saved.Name)
		}
		if _, err := os.Stat(other); err != nil {
			t.Fatalf("retention touched manual backup: %v", err)
		}
		backup.processAutoBackup()
		sameFiles, err := filepath.Glob(filepath.Join("backups", "gost_panel_*.db"))
		requireServiceError(t, err, nil)
		if !reflect.DeepEqual(files, sameFiles) {
			t.Fatalf("same-day auto backup created extra files: %v", sameFiles)
		}
	})
}

func TestBackupDirectoryFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := loadServiceConfig(t, "database:\n  path: live.db\n")
	service := NewBackupService(newServiceTestDB(t))
	cfg.Database.Path = ""
	requireServiceError(t, service.CreateBackup(), bizerrors.ErrDBPathNotConfigured)
	cfg.Database.Path = "live.db"
	service.processAutoBackup()
	if _, err := os.Stat("backups"); !os.IsNotExist(err) {
		t.Fatalf("disabled auto backup touched the disk: %v", err)
	}
	settings, err := service.sysRepo.Get()
	requireServiceError(t, err, nil)
	settings.AutoBackup, settings.BackupRetentionCount = true, 0
	requireServiceError(t, service.sysRepo.Update(settings), nil)
	service.cleanOldBackups("not-created")
	settings.BackupRetentionCount = 1
	requireServiceError(t, service.sysRepo.Update(settings), nil)
	service.cleanOldBackups("not-created")
	requireServiceError(t, os.WriteFile("backups", []byte("not a directory"), 0o600), nil)
	requireServiceError(t, service.CreateBackup(), bizerrors.ErrBackupDirCreateFailed)
	service.processAutoBackup()
}

func TestBackupFailsWhenDatabaseCannotBeSnapshotted(t *testing.T) {
	t.Chdir(t.TempDir())
	loadServiceConfig(t, "database:\n  path: missing-source.db\n")
	db := newServiceTestDB(t)
	// SQLite refuses VACUUM in a transaction. A failed snapshot must not publish a file.
	requireServiceError(t, db.Exec("BEGIN").Error, nil)
	defer db.Exec("ROLLBACK")
	service := NewBackupService(db)
	requireServiceError(t, service.CreateBackup(), bizerrors.ErrBackupFailed)
	files, err := os.ReadDir("backups")
	requireServiceError(t, err, nil)
	if len(files) != 0 {
		t.Fatalf("failed backup left files: %v", files)
	}
}

func TestSendTestEmailSubmitsSMTPEnvelopeAndMessage(t *testing.T) {
	settings := &SystemConfigService{}
	requireServiceError(t, settings.SendTestEmail(&dto.EmailConfigReq{}), bizerrors.ErrSMTPConfigIncomplete)
	for _, tc := range []struct {
		name      string
		to        string
		recipient string
	}{
		{"default recipient", "", "sender@example.test"},
		{"explicit recipient", "recipient@example.test", "recipient@example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			requireServiceError(t, err, nil)
			defer listener.Close()
			result := make(chan error, 1)
			go func() { result <- receiveTestEmail(listener, tc.recipient) }()
			req := &dto.EmailConfigReq{
				Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
				Username: "test-user", Password: "test-password", FromEmail: "sender@example.test", ToEmail: tc.to,
			}
			requireServiceError(t, settings.SendTestEmail(req), nil)
			requireServiceError(t, <-result, nil)
		})
	}
}

func receiveTestEmail(listener net.Listener, recipient string) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	if _, err := fmt.Fprint(conn, "220 test SMTP ready\r\n"); err != nil {
		return err
	}
	for _, exchange := range []struct{ command, reply string }{
		{"EHLO localhost", "250-test\r\n250 AUTH PLAIN\r\n"},
		{"AUTH PLAIN ", "235 authentication accepted\r\n"},
		{"MAIL FROM:<sender@example.test>", "250 sender accepted\r\n"},
		{"RCPT TO:<" + recipient + ">", "250 recipient accepted\r\n"},
		{"DATA", "354 send data\r\n"},
	} {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, exchange.command) {
			return fmt.Errorf("SMTP command = %q, want prefix %q", line, exchange.command)
		}
		if _, err := fmt.Fprint(conn, exchange.reply); err != nil {
			return err
		}
	}
	var body strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == ".\r\n" {
			break
		}
		body.WriteString(line)
	}
	if !strings.Contains(body.String(), "To: "+recipient+"\r\n") || !strings.Contains(body.String(), "Subject: yagp") {
		return fmt.Errorf("unexpected SMTP message %q", body.String())
	}
	if _, err := fmt.Fprint(conn, "250 message accepted\r\n"); err != nil {
		return err
	}
	if line, err := reader.ReadString('\n'); err != nil || line != "QUIT\r\n" {
		return fmt.Errorf("SMTP quit = %q, %v", line, err)
	}
	_, err = fmt.Fprint(conn, "221 goodbye\r\n")
	return err
}
