package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadAssetVerifiesBeforeExtracting(t *testing.T) {
	const contents = "fixture binary"
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tarFile := tar.NewWriter(gz)
	if err := tarFile.WriteHeader(&tar.Header{Name: "gost", Mode: 0700, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarFile.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := tarFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()
	client := Client{http: server.Client()}
	var result bytes.Buffer
	metadata := asset{Size: int64(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("wrong digest")))}
	if err := client.downloadAsset(context.Background(), server.URL, metadata, "gost", &result); err == nil || result.Len() != 0 {
		t.Fatalf("a corrupt archive reached extraction: size = %d, err = %v", result.Len(), err)
	}
	metadata.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if err := client.downloadAsset(context.Background(), server.URL, metadata, "gost", &result); err != nil || result.String() != contents {
		t.Fatalf("valid archive: binary = %q, err = %v", result.String(), err)
	}
}

func TestIsNewerStableRelease(t *testing.T) {
	for _, tt := range []struct {
		component, current, target string
		want                       bool
	}{
		{"gost", "3.2.6", "v3.3.3", true},
		{"gost", "v3.3.3", "v3.2.6", false},
		{"gost", "3.2.6", "v3.3.3-rc.1", false},
		{"daemon", "0.1.99-fffffff", "0.1.100-aaaaaaa", true},
		{"daemon", "0.1.100-aaaaaaa", "0.1.100-fffffff", false},
		{"daemon", "dev", "0.1.100-fffffff", false},
	} {
		if got := IsNewer(tt.component, tt.current, tt.target); got != tt.want {
			t.Errorf("%s %s -> %s: newer = %v, want %v", tt.component, tt.current, tt.target, got, tt.want)
		}
	}
}
