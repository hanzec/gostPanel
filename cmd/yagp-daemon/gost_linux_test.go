package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareGostValidatesDownloadBeforeInstalling(t *testing.T) {
	archive := func(t *testing.T, name string) []byte {
		t.Helper()
		var data bytes.Buffer
		compressed := gzip.NewWriter(&data)
		tarball := tar.NewWriter(compressed)
		if err := tarball.WriteHeader(&tar.Header{Name: name, Size: 6, Mode: 0700}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write([]byte("binary")); err != nil {
			t.Fatal(err)
		}
		if err := tarball.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	for _, test := range []struct {
		name string
		code int
		body []byte
		err  string
	}{
		{"valid", 200, archive(t, "gost"), ""},
		{"wrong archive", 200, archive(t, "other"), "no executable"},
		{"invalid gzip", 200, []byte("invalid"), "EOF"},
		{"missing download", 404, nil, "404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || !strings.Contains(req.URL.Path, "gost_"+gostVersion+"_linux_") {
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
