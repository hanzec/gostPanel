package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
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

// Keep the production URLs and proxy boundary visible to a real local HTTP server.
type gostReleaseTransport struct {
	origin *url.URL
	next   http.RoundTripper
}

func (tr gostReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.Host = req.URL.Host
	local.URL.Scheme, local.URL.Host = tr.origin.Scheme, tr.origin.Host
	return tr.next.RoundTrip(local)
}

func routeGostDownloads(t *testing.T, server *httptest.Server) {
	t.Helper()
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = gostReleaseTransport{origin: origin, next: server.Client().Transport}
	t.Setenv("GH_PROXY", "https://proxy.test/")
}

func writeGostReleaseMetadata(w http.ResponseWriter, archive []byte) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tag_name": "v3.3.3",
		"assets": []map[string]any{{
			"name":   "gost_3.3.3_linux_" + runtime.GOARCH + ".tar.gz",
			"digest": fmt.Sprintf("sha256:%x", sha256.Sum256(archive)),
			"size":   len(archive),
		}},
	})
}
