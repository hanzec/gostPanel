package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yagp/pkg/geoip"
	"yagp/pkg/logger"
)

const countryDatabaseFixture = "../../test/go/fixtures/geoip/GeoIP2-Country-Test.mmdb"

func TestStartGeoIPRefreshesEveryStartupWithoutWaiting(t *testing.T) {
	data, err := os.ReadFile(countryDatabaseFixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "country.mmdb")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	for startup := 1; startup <= 2; startup++ {
		logPath := geoIPStartupLog(t)
		returned := make(chan *geoip.Database, 1)
		go func() { returned <- startGeoIP(ctx, path, server.URL) }()
		var db *geoip.Database
		select {
		case db = <-returned:
		case <-time.After(3 * time.Second):
			t.Fatal("startup waited for the background download")
		}
		if country, err := db.Country(t.Context(), netip.MustParseAddr("81.2.69.142")); country != "GB" || err != nil {
			t.Fatalf("cached country = %q, %v", country, err)
		}
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("startup did not refresh the cached database")
		}
		release <- struct{}{}
		logs := waitGeoIPStartupLog(t, logPath, "GeoIP 数据库下载更新成功")
		if !strings.Contains(logs, "GeoIP 本地数据库已加载") || !strings.Contains(logs, "GeoIP 数据库后台下载开始") || !strings.Contains(logs, strings.Trim(strconv.Quote(path), `"`)) {
			t.Fatalf("missing startup status or path: %s", logs)
		}
		if got := requests.Load(); got != int32(startup) {
			t.Fatalf("after %d startups downloads = %d", startup, got)
		}
	}
}

func TestStartGeoIPDownloadsMissingDatabase(t *testing.T) {
	logPath := geoIPStartupLog(t)
	path := filepath.Join(t.TempDir(), "country.mmdb")
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			http.ServeFile(w, r, countryDatabaseFixture)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	returned := make(chan *geoip.Database, 1)
	go func() { returned <- startGeoIP(ctx, path, server.URL) }()
	var db *geoip.Database
	select {
	case db = <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("startup without a cached database waited for the download")
	}
	if db.Ready() {
		t.Fatal("database was ready before the download finished")
	}
	close(release)
	logs := waitGeoIPStartupLog(t, logPath, "GeoIP 数据库下载更新成功")
	if !strings.Contains(logs, "GeoIP 本地数据库不可用，暂时使用在线查询") || !strings.Contains(logs, strings.Trim(strconv.Quote(path), `"`)) {
		t.Fatalf("missing initial database status: %s", logs)
	}
	if country, err := db.Country(t.Context(), netip.MustParseAddr("2001:218::1")); country != "JP" || err != nil {
		t.Fatalf("downloaded country = %q, %v", country, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("downloaded database was not saved: %v", err)
	}
}

func TestStartGeoIPLogsDownloadFailureState(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "no cache"
		if cached {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) {
			logPath := geoIPStartupLog(t)
			path := filepath.Join(t.TempDir(), "country.mmdb")
			if cached {
				data, err := os.ReadFile(countryDatabaseFixture)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			db := startGeoIP(t.Context(), path, server.URL)
			message := "GeoIP 数据库下载更新失败，使用在线查询"
			if cached {
				message = "GeoIP 数据库下载更新失败，继续使用本地缓存"
			}
			logs := waitGeoIPStartupLog(t, logPath, message)
			if !strings.Contains(logs, `"level":"warn"`) || !strings.Contains(logs, strings.Trim(strconv.Quote(path), `"`)) || !strings.Contains(logs, "503") {
				t.Fatalf("missing failure warning, path, or error: %s", logs)
			}
			if db.Ready() != cached {
				t.Fatalf("database ready = %v, cached = %v", db.Ready(), cached)
			}
		})
	}
}

func geoIPStartupLog(t *testing.T) string {
	t.Helper()
	logFile, err := os.CreateTemp(t.TempDir(), "startup-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = logger.Init(&logger.Config{Level: "error"})
		_ = logFile.Close()
	})
	stdout := os.Stdout
	os.Stdout = logFile
	err = logger.Init(&logger.Config{Level: "info"})
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	return logFile.Name()
}

func waitGeoIPStartupLog(t *testing.T, path, message string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), message) {
			return string(data)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("missing log %q: %s", message, data)
		case <-ticker.C:
		}
	}
}
