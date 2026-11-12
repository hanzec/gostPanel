package geoip

import (
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
)

const testDatabasePath = "testdata/GeoIP2-Country-Test.mmdb"

func TestDatabaseCountryUsesLocalFile(t *testing.T) {
	db := NewDatabase(testDatabasePath, ":invalid-url")
	for _, tc := range []struct {
		address string
		want    string
	}{
		{"81.2.69.142", "GB"},
		{"::ffff:81.2.69.142", "GB"},
		{"2001:218::1", "JP"},
		{"192.0.2.7", ""},
	} {
		got, err := db.Country(t.Context(), netip.MustParseAddr(tc.address))
		if err != nil || got != tc.want {
			t.Errorf("Country(%s) = %q, %v; want %q", tc.address, got, err, tc.want)
		}
	}
}

func TestDatabaseDownloadsMissingFileAndReusesIt(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if downloads.Add(1) == 1 {
			_, _ = w.Write([]byte("incomplete database"))
			return
		}
		http.ServeFile(w, r, testDatabasePath)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "data", "country.mmdb")
	db := NewDatabase(path, server.URL)
	for _, address := range []string{"10.0.0.1", "::ffff:10.0.0.1", "fe80::1%eth0", "::1"} {
		if code, err := db.Country(t.Context(), netip.MustParseAddr(address)); code != "" || err != nil {
			t.Fatalf("Country(%s) = %q, %v", address, code, err)
		}
	}
	if downloads.Load() != 0 {
		t.Fatal("local addresses triggered a download")
	}

	ip := netip.MustParseAddr("81.2.69.142")
	if _, err := db.Country(t.Context(), ip); err == nil {
		t.Fatal("invalid download succeeded")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid download left a database at %s: %v", path, err)
	}
	if code, err := db.Country(t.Context(), ip); code != "GB" || err != nil {
		t.Fatalf("retry = %q, %v", code, err)
	}
	if code, err := NewDatabase(path, "").Country(t.Context(), ip); code != "GB" || err != nil {
		t.Fatalf("lookup after reopening = %q, %v", code, err)
	}
	if got := downloads.Load(); got != 2 {
		t.Fatalf("downloads = %d, want 2", got)
	}
}

func TestDatabaseDownloadContinuesAfterCallerCancels(t *testing.T) {
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
		http.ServeFile(w, r, testDatabasePath)
	}))
	defer server.Close()
	defer finishDownload()
	db := NewDatabase(filepath.Join(t.TempDir(), "country.mmdb"), server.URL)
	ip := netip.MustParseAddr("2001:218::1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	if _, err := db.Country(ctx, ip); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup: %v", err)
	}
	finishDownload()
	if code, err := db.Country(t.Context(), ip); code != "JP" || err != nil {
		t.Fatalf("lookup after cancellation = %q, %v", code, err)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("downloads = %d, want 1", got)
	}
}
