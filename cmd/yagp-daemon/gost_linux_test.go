package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yagp/internal/buildinfo"
)

const gostFixtureReleasePath = "/https://api.github.com/repos/go-gost/gost/releases/latest"

func TestPrepareGostValidatesDownloadBeforeInstalling(t *testing.T) {
	wantPath := "/https://github.com/go-gost/gost/releases/download/v3.3.3/gost_3.3.3_linux_" + runtime.GOARCH + ".tar.gz"
	for _, test := range []struct {
		name string
		code int
		body []byte
		err  string
	}{
		{"valid", 200, gostTestArchive(t, "gost"), ""},
		{"wrong archive", 200, gostTestArchive(t, "other"), "no executable"},
		{"invalid gzip", 200, []byte("invalid"), "EOF"},
		{"missing download", 404, nil, "404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == gostFixtureReleasePath {
					_, _ = io.WriteString(w, `{"tag_name":"v3.3.3"}`)
					return
				}
				if req.Method != http.MethodGet || req.URL.Path != wantPath {
					t.Errorf("unexpected release request: %s %s", req.Method, req.URL.Path)
				}
				w.WriteHeader(test.code)
				_, _ = w.Write(test.body)
			}))
			t.Cleanup(server.Close)
			t.Setenv("GH_PROXY", server.URL+"/")
			output := filepath.Join(t.TempDir(), "gost")
			err := run(context.Background(), []string{"prepare-gost", "--output", output})
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("prepare Gost = %v, want %q", err, test.err)
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("failed download left an executable: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(output)
			if err != nil || string(body) != "binary" {
				t.Fatalf("installed Gost = %q, %v", body, err)
			}
		})
	}
}

func TestEnsureGostBinaryCLIDownloadsPinnedStableVersion(t *testing.T) {
	archive := gostTestArchive(t, "gost")
	var requests atomic.Int32
	asset := "/https://github.com/go-gost/gost/releases/download/v3.3.3/gost_3.3.3_linux_" + runtime.GOARCH + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != asset {
			t.Errorf("unexpected pinned release request: %s %s", req.Method, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	t.Setenv("GH_PROXY", server.URL+"/")
	dir := t.TempDir()
	args := []string{"ensure-gost-binary", "-c", filepath.Join(dir, "node.yaml")}
	t.Setenv("YAGP_GOST_VERSION", "v3.3.3-nightly")
	if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "must be a stable Gost tag") {
		t.Fatalf("ensure with a nightly version = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gost")); !os.IsNotExist(err) {
		t.Fatalf("rejected release left a binary: %v", err)
	}
	t.Setenv("YAGP_GOST_VERSION", "v3.3.3")
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "gost")); err != nil || string(body) != "binary" || requests.Load() != 1 {
		t.Fatalf("installed Gost = %q, %v, release requests = %d", body, err, requests.Load())
	}
}

func TestEnsureGostBootstrapsMissingBinaryAndService(t *testing.T) {
	if os.Getenv("YAGP_TEST_UPGRADE") != "1" {
		t.Skip("set YAGP_TEST_UPGRADE=1 in a disposable Linux container")
	}
	archive := gostTestArchive(t, "gost")
	var available atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == gostFixtureReleasePath {
			_, _ = io.WriteString(w, `{"tag_name":"v3.3.3"}`)
			return
		}
		if r.URL.Path == "/api/config" {
			if user, pass, ok := r.BasicAuth(); !ok || user != "fixture" || pass != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		if !available.Load() || !strings.Contains(r.URL.Path, "/releases/download/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	t.Setenv("GH_PROXY", server.URL+"/")
	dir := t.TempDir()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "systemctl.log")
	t.Setenv("YAGP_TEST_SYSTEMCTL_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(`#!/bin/sh
case "$1" in
show) echo not-found ;;
is-active) exit 1 ;;
daemon-reload|enable) printf '%s\n' "$*" >> "$YAGP_TEST_SYSTEMCTL_LOG" ;;
*) exit 2 ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	const unitDir = "/etc/systemd/system"
	for _, path := range []string{filepath.Dir(unitDir), unitDir} {
		if err := os.Mkdir(path, 0755); err == nil {
			t.Cleanup(func() { _ = os.Remove(path) })
		} else if !os.IsExist(err) {
			t.Fatal(err)
		}
	}
	nodeID := uint64(time.Now().UnixNano())
	unit := fmt.Sprintf("yagp-gost-%d.service", nodeID)
	unitPath := filepath.Join(unitDir, unit)
	if _, err := os.Lstat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("test unit path is unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(unitPath) })
	configPath := filepath.Join(dir, "node.yaml")
	config := fmt.Sprintf("node_id: %d\nadmin_address: %s\nusername: fixture\npassword: secret\n", nodeID, strings.TrimPrefix(server.URL, "http://"))
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gost.yaml"), []byte("api: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureGost(t.Context(), configPath); err == nil || !strings.Contains(err.Error(), "install Gost binary") {
		t.Fatalf("failed bootstrap download returned %v", err)
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("failed download installed a service unit: %v", err)
	}
	available.Store(true)
	if err := ensureGost(t.Context(), configPath); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "gost")); err != nil || string(data) != "binary" {
		t.Fatalf("bootstrapped Gost executable = %q, %v", data, err)
	}
	definition, err := os.ReadFile(unitPath)
	wantStart := fmt.Sprintf("ExecStart=%q -C %q\n", filepath.Join(dir, "gost"), filepath.Join(dir, "gost.yaml"))
	if err != nil || !strings.Contains(string(definition), wantStart) {
		t.Fatalf("bootstrapped unit has wrong command: %q, %v", definition, err)
	}
	if calls, err := os.ReadFile(logPath); err != nil || string(calls) != "daemon-reload\nenable --now "+unit+"\n" {
		t.Fatalf("bootstrap did not reload and start the service: %q, %v", calls, err)
	}
	if err := ensureGost(t.Context(), configPath); !os.IsExist(err) {
		t.Fatalf("existing unit was overwritten: %v", err)
	}
}

func TestUpgradeInstalledRejectsNonReleaseExecutable(t *testing.T) {
	version := buildinfo.Version
	buildinfo.Version = "dev"
	t.Cleanup(func() { buildinfo.Version = version })
	configPath := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(configPath, []byte("node_id: 42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"--upgrade-installed", "--node-id", "42", "-c", configPath}); err == nil ||
		!strings.Contains(err.Error(), "invalid node, upgrade component or release tag") {
		t.Fatalf("installer accepted an executable without a release version: %v", err)
	}
}

func TestEnsureGostStartsLoadedUnitAndWaitsForAuthenticatedAPI(t *testing.T) {
	ctx := context.Background()
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, password, ok := req.BasicAuth()
		if req.URL.Path != "/api/config" || !ok || user != "fixture" || password != "secret" {
			t.Errorf("unexpected Gost readiness request: %s", req.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(admin.Close)
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YAGP_TEST_SYSTEMCTL_LOG", filepath.Join(dir, "systemctl.log"))
	t.Setenv("YAGP_TEST_LOAD_STATE", "loaded")
	t.Setenv("YAGP_TEST_ENABLE_ERROR", "0")
	script := `#!/bin/sh
case "$1" in
show) printf '%s\n' "$YAGP_TEST_LOAD_STATE" ;;
is-active) exit 1 ;;
enable) printf '%s\n' "$*" >> "$YAGP_TEST_SYSTEMCTL_LOG"; exit "$YAGP_TEST_ENABLE_ERROR" ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "node.yaml")
	data := fmt.Sprintf("node_id: 42\nadmin_address: %s\nusername: fixture\npassword: secret\n", strings.TrimPrefix(admin.URL, "http://"))
	if err := os.WriteFile(config, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureGost(ctx, config); err == nil || !strings.Contains(err.Error(), "load Gost configuration") {
		t.Fatalf("missing config = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gost.yaml"), []byte("api: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gost"), []byte("existing binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureGost(ctx, config); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(os.Getenv("YAGP_TEST_SYSTEMCTL_LOG"))
	if err != nil || string(calls) != "enable --now yagp-gost-42.service\n" {
		t.Fatalf("unexpected service calls: %q, %v", calls, err)
	}
	t.Setenv("YAGP_TEST_ENABLE_ERROR", "3")
	if err := ensureGost(ctx, config); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("enable failure = %v", err)
	}
	t.Setenv("YAGP_TEST_LOAD_STATE", "masked")
	if err := ensureGost(ctx, config); err == nil || !strings.Contains(err.Error(), "masked") {
		t.Fatalf("masked service = %v", err)
	}
}
