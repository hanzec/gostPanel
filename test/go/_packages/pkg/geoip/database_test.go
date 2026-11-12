package geoip

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testDatabasePath = "../../test/go/fixtures/geoip/GeoIP2-Country-Test.mmdb"

func TestDatabaseCountryUsesLoadedLocalFile(t *testing.T) {
	db := NewDatabase(testDatabasePath, ":invalid-url")
	if db.Ready() {
		t.Fatal("new database is ready before loading")
	}
	if err := db.Load(); err != nil {
		t.Fatal(err)
	}
	if !db.Ready() {
		t.Fatal("loaded database is not ready")
	}
	for _, tc := range []struct {
		address string
		want    string
	}{
		{"81.2.69.142", "GB"},
		{"::ffff:81.2.69.142", "GB"},
		{"2001:218::1", "JP"},
		{"192.0.2.7", ""},
		{"10.0.0.1", ""},
		{"fe80::1%eth0", ""},
	} {
		got, err := db.Country(t.Context(), netip.MustParseAddr(tc.address))
		if err != nil || got != tc.want {
			t.Errorf("Country(%s) = %q, %v; want %q", tc.address, got, err, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := db.Country(ctx, netip.MustParseAddr("81.2.69.142")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup: %v", err)
	}
}

func TestDatabaseRefreshDownloadsOnEveryStartupAndReplacesCache(t *testing.T) {
	oldData, newData := countryDatabaseVersions(t)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := oldData
		if downloads.Add(1) > 1 {
			data = newData
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "data", "country.mmdb")
	ip := netip.MustParseAddr("81.2.69.142")
	for startup, want := range []string{"GB", "FR"} {
		db := NewDatabase(path, server.URL)
		loadErr := db.Load()
		if startup == 0 {
			if !errors.Is(loadErr, os.ErrNotExist) {
				t.Fatalf("load missing database: %v", loadErr)
			}
			if _, err := db.Country(t.Context(), ip); !errors.Is(err, ErrDatabaseNotReady) {
				t.Fatalf("unloaded lookup: %v", err)
			}
		} else {
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if code, err := db.Country(t.Context(), ip); code != "GB" || err != nil {
				t.Fatalf("cached lookup before refresh = %q, %v", code, err)
			}
		}
		if downloads.Load() != int32(startup) {
			t.Fatal("loading or looking up a database triggered a download")
		}
		if err := db.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !db.Ready() {
			t.Fatal("refreshed database is not ready")
		}
		for range 3 {
			if code, err := db.Country(t.Context(), ip); code != want || err != nil {
				t.Fatalf("refreshed lookup = %q, %v; want %q", code, err, want)
			}
		}
		if downloads.Load() != int32(startup+1) {
			t.Fatalf("startup %d downloads = %d", startup, downloads.Load())
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, newData) {
		t.Fatalf("updated database was not saved: %v", err)
	}
	reopened := NewDatabase(path, ":invalid-url")
	if err := reopened.Load(); err != nil {
		t.Fatal(err)
	}
	if code, err := reopened.Country(t.Context(), ip); code != "FR" || err != nil {
		t.Fatalf("reopened lookup = %q, %v", code, err)
	}
}

func TestDatabaseRefreshFailurePreservesCurrentDatabase(t *testing.T) {
	oldData, _ := countryDatabaseVersions(t)
	for _, tc := range []struct {
		name   string
		status int
		body   []byte
	}{
		{"download failed", http.StatusServiceUnavailable, nil},
		{"invalid database", http.StatusOK, []byte("incomplete database")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "country.mmdb")
			if err := os.WriteFile(path, oldData, 0600); err != nil {
				t.Fatal(err)
			}
			db := NewDatabase(path, server.URL)
			if err := db.Load(); err != nil {
				t.Fatal(err)
			}
			if err := db.Refresh(t.Context()); err == nil {
				t.Fatal("invalid refresh succeeded")
			}
			if !db.Ready() {
				t.Fatal("refresh failure discarded the loaded database")
			}
			for range 3 {
				if code, err := db.Country(t.Context(), netip.MustParseAddr("81.2.69.142")); code != "GB" || err != nil {
					t.Fatalf("lookup after refresh failure = %q, %v", code, err)
				}
			}
			stored, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(stored, oldData) {
				t.Fatalf("refresh failure changed the cached database: %v", err)
			}
			if downloads.Load() != 1 {
				t.Fatalf("lookups retried the failed download: %d requests", downloads.Load())
			}
		})
	}
}

func TestDatabaseInvalidCacheAndDownloadRemainNotReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("incomplete database"))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "country.mmdb")
	db := NewDatabase(path, server.URL)
	if err := db.Refresh(t.Context()); err == nil {
		t.Fatal("invalid download succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid download left a database file: %v", err)
	}
	if err := os.WriteFile(path, []byte("invalid local database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Load(); err == nil || db.Ready() {
		t.Fatalf("invalid local database became ready: %v", err)
	}
	if _, err := db.Country(t.Context(), netip.MustParseAddr("81.2.69.142")); !errors.Is(err, ErrDatabaseNotReady) {
		t.Fatalf("invalid database lookup: %v", err)
	}
}

func TestDatabaseLookupsDoNotWaitForRefresh(t *testing.T) {
	oldData, newData := countryDatabaseVersions(t)
	for _, cached := range []bool{false, true} {
		name := "missing cache"
		if cached {
			name = "loaded cache"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			finishDownload := func() { releaseOnce.Do(func() { close(release) }) }
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if downloads.Add(1) == 1 {
					close(started)
				}
				<-release
				_, _ = w.Write(newData)
			}))
			defer server.Close()
			defer finishDownload()
			path := filepath.Join(t.TempDir(), "country.mmdb")
			db := NewDatabase(path, server.URL)
			if cached {
				if err := os.WriteFile(path, oldData, 0600); err != nil {
					t.Fatal(err)
				}
				if err := db.Load(); err != nil {
					t.Fatal(err)
				}
			}
			refreshed := make(chan error, 1)
			go func() { refreshed <- db.Refresh(t.Context()) }()
			<-started
			type lookupResult struct {
				code string
				err  error
			}
			lookedUp := make(chan lookupResult, 1)
			go func() {
				code, err := db.Country(t.Context(), netip.MustParseAddr("81.2.69.142"))
				lookedUp <- lookupResult{code, err}
			}()
			select {
			case result := <-lookedUp:
				if cached && (result.code != "GB" || result.err != nil) {
					t.Fatalf("lookup during refresh = %q, %v", result.code, result.err)
				}
				if !cached && !errors.Is(result.err, ErrDatabaseNotReady) {
					t.Fatalf("lookup during initial download: %v", result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("lookup waited for the database download")
			}
			if db.Ready() != cached || downloads.Load() != 1 {
				t.Fatalf("during refresh: ready=%v, downloads=%d", db.Ready(), downloads.Load())
			}
			finishDownload()
			if err := <-refreshed; err != nil {
				t.Fatal(err)
			}
			if code, err := db.Country(t.Context(), netip.MustParseAddr("81.2.69.142")); code != "FR" || err != nil {
				t.Fatalf("lookup after refresh = %q, %v", code, err)
			}
		})
	}
}

func countryDatabaseVersions(t *testing.T) ([]byte, []byte) {
	t.Helper()
	data, err := os.ReadFile(testDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	// The MMDB string prefix 0x42 identifies a two-byte country code, not text in a name.
	updated := bytes.ReplaceAll(data, []byte{0x42, 'G', 'B'}, []byte{0x42, 'F', 'R'})
	return data, updated
}
