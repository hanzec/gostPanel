package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type localReleaseTransport struct {
	origin *url.URL
	next   http.RoundTripper
}

func (t localReleaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	local := request.Clone(request.Context())
	local.Host = request.URL.Host
	local.URL.Scheme, local.URL.Host = t.origin.Scheme, t.origin.Host
	return t.next.RoundTrip(local)
}

func localReleaseClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient()
	client.http.Transport = localReleaseTransport{origin: origin, next: server.Client().Transport}
	return client
}

func TestLatestReleaseValidatesGitHubResponse(t *testing.T) {
	const gostPath = "/repos/go-gost/gost/releases/latest"
	const daemonPath = "/repos/hanzec/yagp/releases/latest"
	for _, test := range []struct {
		name, component, wantPath, body, wantVersion, wantError string
		status                                                  int
	}{
		{name: "stable gost", component: "gost", wantPath: gostPath, body: `{"tag_name":"v3.3.3"}`, wantVersion: "v3.3.3", status: 200},
		{name: "stable daemon", component: "daemon", wantPath: daemonPath, body: `{"tag_name":"0.1.100-abcdef1"}`, wantVersion: "0.1.100-abcdef1", status: 200},
		{name: "draft", component: "gost", wantPath: gostPath, body: `{"tag_name":"v3.3.3","draft":true}`, wantError: "invalid stable release tag", status: 200},
		{name: "prerelease", component: "gost", wantPath: gostPath, body: `{"tag_name":"v3.3.3","prerelease":true}`, wantError: "invalid stable release tag", status: 200},
		{name: "malformed tag", component: "daemon", wantPath: daemonPath, body: `{"tag_name":"0.1.100"}`, wantError: "invalid stable release tag", status: 200},
		{name: "oversized version number", component: "gost", wantPath: gostPath, body: `{"tag_name":"v3.3.18446744073709551616"}`, wantError: "invalid stable release tag", status: 200},
		{name: "invalid JSON", component: "gost", wantPath: gostPath, body: "{", wantError: "unexpected EOF", status: 200},
		{name: "rate limit", component: "gost", wantPath: gostPath, wantError: "HTTP 429", status: 429},
		{name: "unknown component", component: "unknown", wantError: "invalid upgrade component"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := localReleaseClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Host != "api.github.com" || req.URL.Path != test.wantPath || req.Method != http.MethodGet ||
					req.Header.Get("User-Agent") != "yagp-upgrade" {
					t.Errorf("unexpected release request: %s %s%s, user agent %q", req.Method, req.Host, req.URL.Path, req.UserAgent())
					http.NotFound(w, req)
					return
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			version, err := client.Latest(t.Context(), test.component)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || version != "" {
					t.Fatalf("latest release = %q, %v, want %q", version, err, test.wantError)
				}
				return
			}
			if err != nil || version != test.wantVersion {
				t.Fatalf("latest release = %q, %v, want %q", version, err, test.wantVersion)
			}
		})
	}
}

func TestReleaseClientRedirectBoundary(t *testing.T) {
	for _, test := range []struct {
		name, location string
		allowed        bool
	}{
		{"github release host", "https://release-assets.githubusercontent.com/stable", true},
		{"untrusted host", "https://example.org/stable", false},
		{"insecure scheme", "http://github.com/stable", false},
		{"redirect loop", "https://api.github.com/loop", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := localReleaseClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/stable" {
					if req.Host != "release-assets.githubusercontent.com" {
						t.Error("request followed a redirect outside the GitHub release hosts")
					}
					_, _ = io.WriteString(w, `{"tag_name":"v3.3.3"}`)
					return
				}
				http.Redirect(w, req, test.location, http.StatusFound)
			})
			tag, err := client.Latest(t.Context(), "gost")
			if test.allowed && (err != nil || tag != "v3.3.3") {
				t.Fatalf("trusted redirect: %q, %v", tag, err)
			}
			if !test.allowed && (err == nil || !strings.Contains(err.Error(), "unexpected GitHub release redirect")) {
				t.Fatalf("redirect %s was not rejected: %v", test.location, err)
			}
		})
	}
}

func TestReleaseDownloadSelectsAndVerifiesLinuxAssets(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("release archive downloads target Linux amd64 and arm64")
	}
	for _, test := range []struct {
		name, component, tag, repo, assetName, binary, variant, wantError string
	}{
		{name: "gost", component: "gost", tag: "v3.3.3", repo: "go-gost/gost", assetName: "gost_3.3.3_linux_" + runtime.GOARCH + ".tar.gz", binary: "gost"},
		{name: "plain daemon", component: "daemon", tag: "0.1.100-abcdef1", repo: "hanzec/yagp", assetName: "yagp-daemon-linux-" + runtime.GOARCH + ".tar.gz", binary: "yagp-daemon-linux-" + runtime.GOARCH},
		{name: "systemd daemon", component: "daemon", tag: "0.1.100-abcdef1", repo: "hanzec/yagp", assetName: "yagp-daemon-linux-" + runtime.GOARCH + ".tar.gz", binary: "yagp-daemon-systemd-linux-" + runtime.GOARCH, variant: "systemd"},
		{name: "mismatched tag", component: "gost", tag: "v3.3.3", repo: "go-gost/gost", wantError: "does not match the requested version"},
		{name: "missing archive", component: "gost", tag: "v3.3.3", repo: "go-gost/gost", wantError: "does not contain the required Linux archive"},
		{name: "release unavailable", component: "gost", tag: "v3.3.3", repo: "go-gost/gost", wantError: "HTTP 502"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const executable = "fixture executable"
			var data []byte
			meta := release{Tag: test.tag, Assets: []asset{{Name: "README.txt"}}}
			if test.name == "mismatched tag" {
				meta.Tag = "v3.3.2"
			}
			if test.assetName != "" {
				data = buildReleaseTestArchive(t, "./"+test.binary, executable)
				meta.Assets = append(meta.Assets, asset{Name: test.assetName, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), Size: int64(len(data))})
			}
			client := localReleaseClient(t, func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.Host == "api.github.com" && req.URL.Path == "/repos/"+test.repo+"/releases/tags/"+test.tag:
					if test.name == "release unavailable" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					if err := json.NewEncoder(w).Encode(meta); err != nil {
						t.Error(err)
					}
				case test.assetName != "" && req.Host == "github.com" && req.URL.Path == "/"+test.repo+"/releases/download/"+test.tag+"/"+test.assetName:
					_, _ = w.Write(data)
				default:
					t.Errorf("unexpected archive request: %s%s", req.Host, req.URL.Path)
					http.NotFound(w, req)
				}
			})
			var dest bytes.Buffer
			err := client.Download(t.Context(), test.component, test.tag, test.variant, &dest)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || dest.Len() != 0 {
					t.Fatalf("download = %q, %v, want %q", dest.String(), err, test.wantError)
				}
				return
			}
			if err != nil || dest.String() != executable {
				t.Fatalf("download = %q, %v", dest.String(), err)
			}
		})
	}
}

func TestReleaseNamesMatchArchiveAndExecutableVariants(t *testing.T) {
	for _, test := range []struct {
		component, tag, variant, architecture string
		archive, executable                   string
	}{
		{"gost", "v3.3.3", "", "amd64", "gost_3.3.3_linux_amd64.tar.gz", "gost"},
		{"daemon", "0.1.100-abcdef1", "", "arm64", "yagp-daemon-linux-arm64.tar.gz", "yagp-daemon-linux-arm64"},
		{"daemon", "0.1.100-abcdef1", "systemd", "arm64", "yagp-daemon-linux-arm64.tar.gz", "yagp-daemon-systemd-linux-arm64"},
	} {
		archive, executable := releaseNames(test.component, test.tag, test.variant, test.architecture)
		if archive != test.archive || executable != test.executable {
			t.Errorf("release %s/%s %s = %q, %q, want %q, %q", test.component, test.variant, test.architecture, archive, executable, test.archive, test.executable)
		}
	}
}

func TestReleaseDownloadRejectsUnsupportedInput(t *testing.T) {
	client := NewClient()
	for _, input := range []struct{ component, tag string }{
		{"unknown", "v3.3.3"},
		{"gost", "3.3.3"},
		{"gost", "v3.3.3-rc.1"},
		{"daemon", "0.1.100-ABCDEF1"},
	} {
		var dest bytes.Buffer
		err := client.Download(t.Context(), input.component, input.tag, "", &dest)
		if err == nil || !strings.Contains(err.Error(), "unsupported platform or release tag") || dest.Len() != 0 {
			t.Fatalf("download with component %q, tag %q: %q, %v", input.component, input.tag, dest.String(), err)
		}
	}
	if runtime.GOOS != "linux" {
		if err := client.Download(t.Context(), "gost", "v3.3.3", "", io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported platform") {
			t.Fatalf("accepted release download on %s: %v", runtime.GOOS, err)
		}
	}
}

func TestDownloadAssetRejectsMissingMetadataAndTruncatedBody(t *testing.T) {
	const archive = "not a complete archive"
	expectedSize := int64(len(archive) + 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(expectedSize))
		_, _ = io.WriteString(w, archive)
	}))
	defer server.Close()
	client := Client{http: server.Client()}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(archive)))
	for _, metadata := range []asset{
		{Digest: digest, Size: -1},
		{Digest: digest, Size: maxArchiveSize + 1},
		{Digest: "sha256:ff", Size: expectedSize},
		{Digest: "md5:1234", Size: expectedSize},
	} {
		var output bytes.Buffer
		err := client.downloadAsset(t.Context(), server.URL, metadata, "gost", &output)
		if err == nil || !strings.Contains(err.Error(), "no usable SHA-256 digest or size") || output.Len() != 0 {
			t.Fatalf("download with unusable metadata %+v: %q, %v", metadata, output.String(), err)
		}
	}
	var output bytes.Buffer
	err := client.downloadAsset(t.Context(), "://invalid", asset{Digest: digest, Size: expectedSize}, "gost", &output)
	if err == nil || output.Len() != 0 {
		t.Fatalf("download with malformed asset URL: %q, %v", output.String(), err)
	}
	err = client.downloadAsset(t.Context(), server.URL, asset{Digest: digest, Size: expectedSize}, "gost", &output)
	if !errors.Is(err, io.ErrUnexpectedEOF) || output.Len() != 0 {
		t.Fatalf("truncated download reached extraction: %q, %v", output.String(), err)
	}
}

func TestDownloadAssetFailsWhenTemporaryStorageUnavailable(t *testing.T) {
	const archive = "archive"
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(archive)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, archive) }))
	defer server.Close()
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)
	t.Setenv("TMP", blocked)
	t.Setenv("TEMP", blocked)
	client := Client{http: server.Client()}
	var dest bytes.Buffer
	err := client.downloadAsset(t.Context(), server.URL, asset{Digest: digest, Size: int64(len(archive))}, "gost", &dest)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || dest.Len() != 0 {
		t.Fatalf("temporary storage failure: %q, %v", dest.String(), err)
	}
}

func TestExtractBinaryRejectsBrokenOrIncompleteArchives(t *testing.T) {
	var invalidTar bytes.Buffer
	gz := gzip.NewWriter(&invalidTar)
	if _, err := io.WriteString(gz, "truncated tar header"); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, wantError string
		data            []byte
	}{
		{"invalid gzip", "invalid header", []byte("this is not compressed")},
		{"truncated tar", "unexpected EOF", invalidTar.Bytes()},
		{"missing binary", "does not contain the executable", buildReleaseTestArchive(t, "README.txt", "not an executable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dest bytes.Buffer
			err := extractBinary(bytes.NewReader(test.data), "gost", &dest)
			if err == nil || !strings.Contains(err.Error(), test.wantError) || dest.Len() != 0 {
				t.Fatalf("extracting damaged archive: %q, %v, want %q", dest.String(), err, test.wantError)
			}
		})
	}
}

func TestReleaseRequestErrors(t *testing.T) {
	client := localReleaseClient(t, func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected HTTP request: %s %s", req.Method, req.URL)
		http.NotFound(w, req)
	})
	if _, err := client.get(t.Context(), "://invalid"); err == nil {
		t.Fatal("invalid release request URL was accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Latest(ctx, "gost"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled release request: %v", err)
	}
}

func buildReleaseTestArchive(t *testing.T, name, payload string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "extra", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, payload); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
