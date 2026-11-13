package nodebridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateSystemdRuntime(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != controlTestUser || password != controlTestPassword || r.URL.Path != "/api/config" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(admin.Close)
	for _, test := range []struct {
		name       string
		fail       string
		brokenGost bool
	}{
		{name: "installed"},
		{name: "rollback", fail: "enable --now yagp-daemon.service yagp-gost.service"},
		{name: "rollback when Gost is unhealthy", brokenGost: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			unitDir, nodeDir := t.TempDir(), t.TempDir()
			configPath := filepath.Join(nodeDir, "node.yaml")
			logPath := filepath.Join(unitDir, "commands")
			t.Setenv("PATH", unitDir)
			t.Setenv("TEST_SYSTEMCTL_LOG", logPath)
			t.Setenv("TEST_SYSTEMCTL_FAILURE", test.fail)
			writeRuntimeTestFile(t, filepath.Join(unitDir, "systemctl"), `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_SYSTEMCTL_LOG"
test "$*" != "$TEST_SYSTEMCTL_FAILURE"
`)
			writeRuntimeTestFile(t, filepath.Join(unitDir, "nft"), "#!/bin/sh\nexit 0\n")
			for _, name := range []string{"yagp-daemon-42.service", "yagp-gost-42.service"} {
				writeRuntimeTestFile(t, filepath.Join(unitDir, name), "legacy")
			}
			address := strings.TrimPrefix(admin.URL, "http://")
			cfg := Config{NodeID: 42, AdminAddress: address, ObserverListen: address, Username: controlTestUser, Password: controlTestPassword}
			if test.brokenGost {
				cfg.AdminAddress = "192.0.2.1:8080"
			}
			err := migrateSystemdUnits(t.Context(), cfg, configPath, unitDir)
			log, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(log), "nftables.service") || strings.Contains(string(log), "ufw.service") {
				t.Fatalf("migration invoked a host firewall service:\n%s", log)
			}
			if test.fail != "" || test.brokenGost {
				if err == nil || ReadRuntimeLayout(configPath) != 0 || !strings.Contains(string(log), "enable --now yagp-daemon-42.service yagp-gost-42.service") {
					t.Fatalf("failed migration did not restore the legacy layout: %v\n%s", err, log)
				}
				if _, err := os.Stat(filepath.Join(unitDir, "yagp-daemon.service")); !os.IsNotExist(err) {
					t.Fatalf("failed migration left the new daemon unit: %v", err)
				}
				return
			}
			cfg.RuntimeLayout = ReadRuntimeLayout(configPath)
			if err != nil || cfg.RuntimeLayout != RuntimeLayoutVersion || !strings.Contains(string(log), "disable --now yagp-daemon-42.service yagp-gost-42.service") {
				t.Fatalf("migration did not replace the legacy units: %v\n%s", err, log)
			}
			if _, err := os.Stat(filepath.Join(unitDir, "yagp-daemon-42.service")); !os.IsNotExist(err) {
				t.Fatalf("migration left the legacy daemon unit: %v", err)
			}
			gost, err := os.ReadFile(filepath.Join(unitDir, cfg.runtimeUnit("gost")))
			if err != nil || !strings.Contains(string(gost), "BindsTo=yagp-daemon.service\nAfter=yagp-daemon.service\nPartOf=yagp-daemon.service") {
				t.Fatalf("Gost unit is missing the daemon lifecycle dependency: %v\n%s", err, gost)
			}
			daemon, err := os.ReadFile(filepath.Join(unitDir, cfg.runtimeUnit("daemon")))
			if err != nil || !strings.Contains(string(daemon), "After=network.target nftables.service docker.service") {
				t.Fatalf("daemon unit is missing network ordering: %v\n%s", err, daemon)
			}
			if err := migrateSystemdUnits(t.Context(), cfg, configPath, unitDir); err == nil {
				t.Fatal("migration overwrote an installed fixed unit")
			}
			if err := UpdateSystemdRuntime(t.Context(), cfg, configPath); err != nil {
				t.Fatalf("versioned layout was not recognized: %v", err)
			}
			if err := restartAndCheckUpgrade(t.Context(), cfg, cfg.runtimeUnit("daemon"), "daemon", ""); err != nil {
				t.Fatalf("restart of the fixed daemon unit did not wait for both services: %v", err)
			}
		})
	}
}

func TestUpdateSystemdNetworkOrder(t *testing.T) {
	for _, test := range []struct {
		name, rejectedCommand, blockedPath string
	}{
		{name: "installed"},
		{name: "reload failed", rejectedCommand: "daemon-reload"},
		{name: "drop-in directory blocked", blockedPath: "directory"},
		{name: "drop-in file blocked", blockedPath: "file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			unitDir, nodeDir := t.TempDir(), t.TempDir()
			configPath := filepath.Join(nodeDir, "node.yaml")
			cfg := Config{NodeID: 42, RuntimeLayout: singleNodeLayoutVersion}
			unit := filepath.Join(unitDir, cfg.runtimeUnit("daemon"))
			dropIn := filepath.Join(unit+".d", redirectOrderDropIn)
			logPath := filepath.Join(unitDir, "commands")
			t.Setenv("PATH", unitDir)
			t.Setenv("TEST_SYSTEMCTL_LOG", logPath)
			t.Setenv("TEST_SYSTEMCTL_FAILURE", test.rejectedCommand)
			writeRuntimeTestFile(t, unit, "existing unit")
			writeRuntimeTestFile(t, filepath.Join(unitDir, "systemctl"), `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_SYSTEMCTL_LOG"
test "$*" != "$TEST_SYSTEMCTL_FAILURE"
`)
			writeRuntimeTestFile(t, filepath.Join(nodeDir, runtimeLayoutFile), "1\n")
			switch test.blockedPath {
			case "directory":
				writeRuntimeTestFile(t, unit+".d", "unexpected file")
			case "file":
				if err := os.MkdirAll(dropIn, 0755); err != nil {
					t.Fatal(err)
				}
			}
			fail := test.rejectedCommand != "" || test.blockedPath != ""
			err := updateSystemdNetworkOrder(t.Context(), cfg, configPath, unitDir)
			if (err != nil) != fail {
				t.Fatalf("update systemd network order: %v, want failure %t", err, fail)
			}
			wantVersion := RuntimeLayoutVersion
			if fail {
				wantVersion = singleNodeLayoutVersion
			} else {
				order, err := os.ReadFile(dropIn)
				if err != nil || string(order) != "[Unit]\nAfter=nftables.service docker.service\n" {
					t.Fatalf("network ordering = %q, %v", order, err)
				}
			}
			if version := ReadRuntimeLayout(configPath); version != wantVersion {
				t.Fatalf("runtime layout version = %d, want %d", version, wantVersion)
			}
			current, err := os.ReadFile(unit)
			if err != nil || string(current) != "existing unit" {
				t.Fatalf("original unit was changed: %q, %v", current, err)
			}
			want := ""
			if test.blockedPath == "" {
				want = "daemon-reload\n"
			}
			log, err := os.ReadFile(logPath)
			if want == "" && os.IsNotExist(err) {
				err = nil
			}
			if err != nil || string(log) != want {
				t.Fatalf("systemctl commands = %q, %v, want %q", log, err, want)
			}
		})
	}
}

func TestRuntimeUpdateRequiresAnInstalledSingleNode(t *testing.T) {
	if os.Getenv("YAGP_TEST_UPGRADE") != "1" {
		t.Skip("requires an isolated Linux environment with /run/systemd/system")
	}
	unitDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("PATH", unitDir)
	writeRuntimeTestFile(t, filepath.Join(unitDir, "systemd-run"), "#!/bin/sh\nexit 0\n")
	for _, id := range []string{"42", "7"} {
		if err := os.Mkdir(filepath.Join(stateDir, id), 0700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(stateDir, "42", "node.yaml")
	cfg := Config{NodeID: 42}
	if runtimeUpdateRequired(cfg, configPath, unitDir) {
		t.Fatal("migration was offered without a legacy service")
	}
	writeRuntimeTestFile(t, filepath.Join(unitDir, cfg.runtimeUnit("daemon")), "legacy")
	if !runtimeUpdateRequired(cfg, configPath, unitDir) {
		t.Fatal("legacy service did not request a runtime migration")
	}
	writeRuntimeTestFile(t, filepath.Join(stateDir, "7", "yagp-daemon"), "installed")
	if runtimeUpdateRequired(cfg, configPath, unitDir) {
		t.Fatal("migration was offered on a multi-node host")
	}
	if err := UpdateSystemdRuntime(t.Context(), cfg, configPath); err == nil || !strings.Contains(err.Error(), "only one node") {
		t.Fatalf("migration did not reject a second node: %v", err)
	}
}

func TestRuntimeUpdateStatusUsesPersistedVersion(t *testing.T) {
	if os.Getenv("YAGP_TEST_UPGRADE") != "1" {
		t.Skip("requires an isolated Linux environment with /run/systemd/system")
	}
	unitDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("PATH", unitDir)
	for _, executable := range []string{"systemctl", "systemd-run"} {
		writeRuntimeTestFile(t, filepath.Join(unitDir, executable), "#!/bin/sh\nexit 0\n")
	}
	nodeDir := filepath.Join(stateDir, "42")
	if err := os.Mkdir(nodeDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(nodeDir, "node.yaml")
	cfg := Config{NodeID: 42, RuntimeLayout: singleNodeLayoutVersion}
	writeRuntimeTestFile(t, filepath.Join(unitDir, cfg.runtimeUnit("daemon")), "installed")
	writeRuntimeTestFile(t, filepath.Join(nodeDir, runtimeLayoutFile), "1\n")

	if !runtimeUpdateRequired(cfg, configPath, unitDir) {
		t.Fatal("existing single-node layout did not request network-order migration")
	}
	if err := updateSystemdNetworkOrder(t.Context(), cfg, configPath, unitDir); err != nil {
		t.Fatal(err)
	}
	if runtimeUpdateRequired(cfg, configPath, unitDir) {
		t.Fatal("persisted version did not clear the update status for the running daemon")
	}
}

func TestSingleNodeRuntimeIgnoresUninstalledConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	other := filepath.Join(root, "7")
	for _, directory := range []string{dir, other, filepath.Join(root, ".install.backup")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeRuntimeTestFile(t, filepath.Join(dir, "yagp-daemon"), "installed")
	writeRuntimeTestFile(t, filepath.Join(other, "node.yaml"), "uninstalled")
	if err := checkSingleNode(dir); err != nil {
		t.Fatal(err)
	}
	writeRuntimeTestFile(t, filepath.Join(other, "yagp-daemon"), "installed")
	if err := checkSingleNode(dir); err == nil {
		t.Fatal("the runtime migration accepted two installed nodes")
	}
}

func writeRuntimeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
}
