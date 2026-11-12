package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
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

func TestDownloadGostPreservesInstalledBinaryOnVerificationOrExtractionFailure(t *testing.T) {
	var incomplete bytes.Buffer
	compressed := gzip.NewWriter(&incomplete)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "gost", Size: 20, Mode: 0700}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, "partial binary"); err != nil {
		t.Fatal(err)
	}
	// A complete gzip stream can still contain a truncated tar entry.
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	trustedArchive := gostTestArchive(t, "gost")
	tamperedArchive := bytes.Clone(trustedArchive)
	tamperedArchive[len(tamperedArchive)-1] ^= 0xff
	for _, test := range []struct {
		name, wantError string
		body            []byte
		metadataFailure bool
	}{
		{name: "unavailable trusted metadata", metadataFailure: true, wantError: "HTTP 503"},
		{name: "archive digest mismatch", body: tamperedArchive, wantError: "SHA-256 or size does not match"},
		{name: "verified archive with truncated executable", body: incomplete.Bytes(), wantError: "unexpected EOF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Host == "api.github.com" && req.URL.Path == "/repos/go-gost/gost/releases/tags/v3.3.3" {
					if test.metadataFailure {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					metadata := test.body
					if test.name == "archive digest mismatch" {
						metadata = trustedArchive
					}
					writeGostReleaseMetadata(w, metadata)
					return
				}
				wantPath := "/https://github.com/go-gost/gost/releases/download/v3.3.3/gost_3.3.3_linux_" + runtime.GOARCH + ".tar.gz"
				if test.metadataFailure || req.Host != "proxy.test" || req.URL.Path != wantPath {
					t.Errorf("unexpected download request: %s%s", req.Host, req.URL.Path)
					http.NotFound(w, req)
					return
				}
				_, _ = w.Write(test.body)
			}))
			t.Cleanup(server.Close)
			routeGostDownloads(t, server)
			t.Setenv("YAGP_GOST_VERSION", "v3.3.3")
			output := filepath.Join(t.TempDir(), "gost")
			if err := os.WriteFile(output, []byte("installed working Gost"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := downloadGost(t.Context(), output, false); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("failed Gost download = %v, want %q", err, test.wantError)
			}
			if body, err := os.ReadFile(output); err != nil || string(body) != "installed working Gost" {
				t.Fatalf("failed update replaced installed binary: %q, %v", body, err)
			}
			if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("failed update changed installed permissions: %v, %v", info, err)
			}
			if leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".gost-*")); err != nil || len(leftovers) != 0 {
				t.Fatalf("failed update left temporary files: %v, %v", leftovers, err)
			}
		})
	}
}

func TestPrepareGostValidatesDownloadBeforeInstalling(t *testing.T) {
	wantPath := "/https://github.com/go-gost/gost/releases/download/v3.3.3/gost_3.3.3_linux_" + runtime.GOARCH + ".tar.gz"
	for _, test := range []struct {
		name string
		code int
		body []byte
		err  string
	}{
		{"valid", 200, gostTestArchive(t, "gost"), ""},
		{"wrong archive", 200, gostTestArchive(t, "other"), "does not contain the executable"},
		{"invalid gzip", 200, []byte("invalid"), "EOF"},
		{"missing download", 404, nil, "404"},
		{"tampered archive", 200, gostTestArchive(t, "gost"), "SHA-256 or size does not match"},
		{"trusted metadata unavailable", 200, gostTestArchive(t, "gost"), "HTTP 503"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Host == "proxy.test" && req.URL.Path == gostFixtureReleasePath {
					_, _ = io.WriteString(w, `{"tag_name":"v3.3.3"}`)
					return
				}
				if req.Host == "api.github.com" && req.URL.Path == "/repos/go-gost/gost/releases/tags/v3.3.3" {
					if test.name == "trusted metadata unavailable" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					data := test.body
					if test.code != http.StatusOK {
						data = gostTestArchive(t, "gost")
					}
					writeGostReleaseMetadata(w, data)
					return
				}
				if req.Host != "proxy.test" || req.Method != http.MethodGet || req.URL.Path != wantPath {
					t.Errorf("unexpected release request: %s %s%s", req.Method, req.Host, req.URL.Path)
					http.NotFound(w, req)
					return
				}
				w.WriteHeader(test.code)
				if test.name == "tampered archive" {
					_, _ = w.Write(gostTestArchive(t, "replacement"))
					return
				}
				_, _ = w.Write(test.body)
			}))
			t.Cleanup(server.Close)
			routeGostDownloads(t, server)
			output := filepath.Join(t.TempDir(), "gost")
			err := run(context.Background(), []string{"prepare-gost", "--output", output})
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("prepare Gost = %v, want %q", err, test.err)
				}
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("failed download left an executable: %v", err)
				}
				leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".gost-*"))
				if err != nil || len(leftovers) != 0 {
					t.Fatalf("failed download left temporary files: %v, %v", leftovers, err)
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host == "api.github.com" && req.URL.Path == "/repos/go-gost/gost/releases/tags/v3.3.3" {
			writeGostReleaseMetadata(w, archive)
			return
		}
		if req.Method != http.MethodGet || req.Host != "proxy.test" || req.URL.Path != asset {
			t.Errorf("unexpected pinned release request: %s %s%s", req.Method, req.Host, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	routeGostDownloads(t, server)
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

func TestPrepareGostRetriesGatewayTimeout(t *testing.T) {
	archive := gostTestArchive(t, "gost")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host == "api.github.com" {
			writeGostReleaseMetadata(w, archive)
			return
		}
		if requests.Add(1) == 1 {
			http.Error(w, "temporary gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	routeGostDownloads(t, server)
	t.Setenv("YAGP_GOST_VERSION", "v3.3.3")
	output := filepath.Join(t.TempDir(), "gost")
	if err := run(t.Context(), []string{"prepare-gost", "--output", output}); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("archive download made %d requests, want 2", got)
	}
}

func TestPrepareGostStopsRetryWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	archive := gostTestArchive(t, "gost")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host == "api.github.com" {
			writeGostReleaseMetadata(w, archive)
			return
		}
		cancel()
		http.Error(w, "canceled download", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	routeGostDownloads(t, server)
	t.Setenv("YAGP_GOST_VERSION", "v3.3.3")
	output := filepath.Join(t.TempDir(), "gost")
	if err := run(ctx, []string{"prepare-gost", "--output", output}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled installation = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("canceled download left an executable: %v", err)
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
		if r.Host == "api.github.com" && r.URL.Path == "/repos/go-gost/gost/releases/tags/v3.3.3" {
			writeGostReleaseMetadata(w, archive)
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
	routeGostDownloads(t, server)
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
