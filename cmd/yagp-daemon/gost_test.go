package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func gostTestArchive(t *testing.T, name string) []byte {
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

func TestDownloadGostRetriesGatewayTimeout(t *testing.T) {
	archive := gostTestArchive(t, "gost")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	output := filepath.Join(t.TempDir(), "gost")
	if err := downloadGostWithCurl(t.Context(), server.URL, output); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("download made %d requests, want 2", got)
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "binary" {
		t.Fatalf("installed Gost = %q, %v", data, err)
	}
}
