package nodebridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yagp/internal/buildinfo"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func writeUpgradeFixture(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func gostVersionFixture(version string) string {
	return fmt.Sprintf("#!/bin/sh\nprintf 'gost %%s\\n' %s\n", version)
}

func TestInstallGostUpgradeTransaction(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()
	cfg := Config{NodeID: 42, AdminAddress: strings.TrimPrefix(admin.URL, "http://")}
	for _, test := range []struct {
		name, candidate, target, fail, wantError, installed string
		calls                                               []string
	}{
		{"success", "3.3.3", "v3.3.3", "", "", "v3.3.3", []string{"restart"}},
		{"same version", "", "v3.3.2", "", "", "v3.3.2", nil},
		{"downgrade", "", "v3.3.1", "", "not newer", "v3.3.2", nil},
		{"wrong candidate", "3.3.4", "v3.3.3", "", "staged executable reports version", "v3.3.2", nil},
		{"rollback", "3.3.3", "v3.3.3", "once", "previous version was restored", "v3.3.2", []string{"restart", "reset-failed", "restart"}},
		{"rollback restart fails", "3.3.3", "v3.3.3", "always", "restart previous version", "v3.3.2", []string{"restart", "reset-failed", "restart"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "gost")
			writeUpgradeFixture(t, target, gostVersionFixture("3.3.2"))
			bin := t.TempDir()
			callLog := filepath.Join(bin, "systemctl.log")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("YAGP_UPGRADE_TEST_CALLS", callLog)
			t.Setenv("YAGP_UPGRADE_TEST_FAILURE", test.fail)
			writeUpgradeFixture(t, filepath.Join(bin, "systemctl"), `#!/bin/sh
printf '%s %s\n' "$1" "$2" >> "$YAGP_UPGRADE_TEST_CALLS"
if [ "$1" = restart ]; then
  case "$YAGP_UPGRADE_TEST_FAILURE" in
    always) echo 'restart rejected' >&2; exit 1 ;;
    once)
      if [ ! -e "$YAGP_UPGRADE_TEST_CALLS.failed" ]; then
        : > "$YAGP_UPGRADE_TEST_CALLS.failed"
        echo 'restart rejected' >&2
        exit 1
      fi ;;
  esac
fi
`)
			err := installUpgrade(context.Background(), cfg, dir, "gost", test.target, func(w io.Writer) error {
				if test.candidate == "" {
					t.Fatal("download attempted without a valid newer target")
				}
				_, err := io.WriteString(w, gostVersionFixture(test.candidate))
				return err
			})
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("upgrade error = %v, want %q", err, test.wantError)
			}
			version, err := executableVersion(context.Background(), target, "gost")
			if err != nil || version != test.installed {
				t.Fatalf("installed version = %q, %v, want %s", version, err, test.installed)
			}
			var wantCalls []string
			for _, action := range test.calls {
				wantCalls = append(wantCalls, action+" yagp-gost-42.service")
			}
			data, err := os.ReadFile(callLog)
			if err != nil && (len(wantCalls) != 0 || !os.IsNotExist(err)) {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(data)); got != strings.Join(wantCalls, "\n") {
				t.Fatalf("service manager calls = %q, want %v", got, wantCalls)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging files left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestUpgradeSystemdWorkerRPC(t *testing.T) {
	if os.Getenv("YAGP_TEST_UPGRADE") != "1" {
		t.Skip("set YAGP_TEST_UPGRADE=1 on a systemd node or in a disposable Linux container with /run/systemd/system")
	}
	dir := filepath.Join(t.TempDir(), "node files")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node.yaml")
	bin := t.TempDir()
	callLog := filepath.Join(bin, "systemd-run.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YAGP_UPGRADE_TEST_CALLS", callLog)
	writeUpgradeFixture(t, filepath.Join(bin, "systemd-run"), `#!/bin/sh
printf '%s\n' "$@" > "$YAGP_UPGRADE_TEST_CALLS"
if [ "$YAGP_UPGRADE_TEST_FAILURE" = fail ]; then
  echo 'worker launch failed' >&2
  exit 1
fi
`)
	writeUpgradeFixture(t, filepath.Join(bin, "systemctl"), `#!/bin/sh
case "$1" in
  is-active) echo inactive; exit 3 ;;
  restart) exit 0 ;;
  *) exit 1 ;;
esac
`)
	if !supportsUpgrade(path) {
		t.Fatal("test requires /run/systemd/system and an executable systemd-run fixture")
	}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer admin.Close()
	cfg := Config{NodeID: 42, ConfigFile: path, AdminAddress: strings.TrimPrefix(admin.URL, "http://"), Username: controlTestUser, Password: controlTestPassword}
	control := controlServer{config: cfg}
	ctx := t.Context()
	request := &nodegrpc.NodeUpgradeRequest{Component: "gost", Version: "v3.3.3"}
	state, err := control.StartUpgrade(ctx, request)
	if err != nil || !state.GetSupported() || state.GetState() != "queued" || state.GetTargetVersion() != "v3.3.3" {
		t.Fatalf("launch status = %v, %v", state, err)
	}
	args, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--unit=yagp-upgrade-42.service", "--collect", "--property=Type=exec", "--working-directory=" + dir,
		"--", filepath.Join(dir, "yagp-daemon"), "upgrade-component", "-c", path, "--component", "gost", "--tag", "v3.3.3"}
	if got := strings.Split(strings.TrimSpace(string(args)), "\n"); !slices.Equal(got, wantArgs) {
		t.Fatalf("worker arguments = %v, want %v", got, wantArgs)
	}
	t.Setenv("YAGP_UPGRADE_TEST_FAILURE", "fail")
	if _, err := control.StartUpgrade(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("failed systemd-run returned %v", err)
	}
	t.Setenv("YAGP_UPGRADE_TEST_FAILURE", "")
	writeUpgradeFixture(t, filepath.Join(dir, "gost"), gostVersionFixture("3.3.2"))
	stale := UpgradeStatus{Component: "gost", Tag: "v3.3.3", State: "running"}
	if err := writeUpgradeStatus(dir, stale); err != nil {
		t.Fatal(err)
	}
	state, err = control.GetUpgradeStatus(ctx, &emptypb.Empty{})
	if err != nil || state.GetState() != "failed" || state.GetError() != "upgrade worker stopped before reporting its result" ||
		state.GetGostVersion() != "v3.3.2" {
		t.Fatalf("orphan worker status = %v, %v", state, err)
	}
	for _, test := range []struct {
		candidate, wantState string
	}{
		{"3.3.4", "failed"},
		{"3.3.3", "succeeded"},
	} {
		err := runUpgrade(ctx, cfg, path, "gost", "v3.3.3", func(w io.Writer) error {
			operation, err := readUpgradeOperation(dir)
			if err != nil || operation.State != "running" || operation.Tag != "v3.3.3" {
				t.Fatalf("operation while staging = %+v, %v", operation, err)
			}
			_, err = io.WriteString(w, gostVersionFixture(test.candidate))
			return err
		})
		if (err != nil) != (test.wantState == "failed") {
			t.Fatalf("upgrade with candidate %s returned %v", test.candidate, err)
		}
		state, err = control.GetUpgradeStatus(ctx, &emptypb.Empty{})
		if err != nil || state.GetState() != test.wantState || state.GetTargetVersion() != "v3.3.3" {
			t.Fatalf("completed worker status = %v, %v", state, err)
		}
	}
}

func TestInstalledDaemonUpgradePreservesEnrollment(t *testing.T) {
	if os.Getenv("YAGP_TEST_UPGRADE") != "1" {
		t.Skip("set YAGP_TEST_UPGRADE=1 in a disposable Linux container with /run/systemd/system")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeUpgradeFixture(t, filepath.Join(bin, "systemd-run"), "#!/bin/sh\nexit 0\n")
	logPath := filepath.Join(bin, "systemctl.log")
	t.Setenv("YAGP_UPGRADE_TEST_CALLS", logPath)
	writeUpgradeFixture(t, filepath.Join(bin, "systemctl"), `#!/bin/sh
printf '%s %s\n' "$1" "$2" >> "$YAGP_UPGRADE_TEST_CALLS"
`)

	version := buildinfo.Version
	buildinfo.Version = expectedDaemonUpgradeVersion
	t.Cleanup(func() { buildinfo.Version = version })
	listener := listenLocal(t)
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	ready := health.NewServer()
	service := daemonHealthService + buildinfo.Version
	ready.SetServingStatus(service, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, ready)
	go func() { _ = server.Serve(listener) }()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "node.yaml")
	cfg := Config{NodeID: 42, ConfigFile: configPath, ObserverListen: listener.Addr().String()}
	files := map[string]string{
		"node.yaml":          "node_id: 42\n",
		PanelCredentialsFile: "refresh_token: enrolled\n",
		"gost.yaml":          "api: {}\n",
		"nft.json":           `{"nftables":[]}`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const oldVersion = "0.1.98-abcdef0"
	target := filepath.Join(dir, "yagp-daemon")
	oldBinary := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %s\n", oldVersion)
	candidate := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %s\n", buildinfo.Version)
	if err := UpgradeInstalledDaemon(t.Context(), cfg, filepath.Join(dir, "missing", "node.yaml"), strings.NewReader(candidate)); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "record upgrade start") {
		t.Fatalf("upgrade could proceed without a writable state directory: %v", err)
	}
	writeUpgradeFixture(t, target, oldBinary)
	requestFailure := errors.New("release network unavailable")
	func() {
		transport := http.DefaultTransport
		http.DefaultTransport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, requestFailure
		}}
		defer func() { http.DefaultTransport = transport }()
		if err := RunUpgrade(t.Context(), cfg, configPath, "daemon", buildinfo.Version); !errors.Is(err, requestFailure) {
			t.Fatalf("release download error = %v", err)
		}
	}()
	if installed, err := executableVersion(t.Context(), target, "daemon"); err != nil || installed != oldVersion {
		t.Fatalf("release download failure changed installed daemon to %q, %v", installed, err)
	}
	if err := UpgradeInstalledDaemon(t.Context(), cfg, configPath, strings.NewReader(candidate)); err != nil {
		t.Fatal(err)
	}
	if installed, err := executableVersion(t.Context(), target, "daemon"); err != nil || installed != buildinfo.Version {
		t.Fatalf("installed daemon = %q, %v", installed, err)
	}

	writeUpgradeFixture(t, target, oldBinary)
	ready.SetServingStatus(service, healthpb.HealthCheckResponse_NOT_SERVING)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if err := UpgradeInstalledDaemon(ctx, cfg, configPath, strings.NewReader(candidate)); err == nil || !strings.Contains(err.Error(), "previous version was restored") {
		t.Fatalf("unready daemon upgrade = %v", err)
	}
	if installed, err := executableVersion(t.Context(), target, "daemon"); err != nil || installed != oldVersion {
		t.Fatalf("rollback daemon = %q, %v", installed, err)
	}
	state, err := readUpgradeOperation(dir)
	if err != nil || state.State != "failed" || state.Component != "daemon" || state.Tag != buildinfo.Version {
		t.Fatalf("rollback operation = %+v, %v", state, err)
	}
	if calls, err := os.ReadFile(logPath); err != nil || string(calls) != "restart yagp-daemon-42.service\nrestart yagp-daemon-42.service\nreset-failed yagp-daemon-42.service\nrestart yagp-daemon-42.service\n" {
		t.Fatalf("daemon service operations = %q, %v", calls, err)
	}
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Fatalf("daemon upgrade changed %s: %v", name, err)
		}
	}
}
