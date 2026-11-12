package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"yagp/internal/buildinfo"
	"yagp/internal/deployjob"
	"yagp/pkg/upgrade"
)

func TestNativeToolUpdateNeverDownloadsOrExecutesScripts(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "0.1.146-abcdef0"
	t.Cleanup(func() { buildinfo.Version = previous })
	const tag = "0.1.147-4713edf"
	archive := "yagp-depoly-linux-" + runtime.GOARCH + ".tar.gz"
	base := upgrade.YAGPReleaseCache + "/releases/download/" + tag + "/"
	for _, tc := range []struct {
		name, metadata, failed, want string
		requests                     int
	}{
		{"already current", `{"tag":"0.1.146-abcdef0"}`, "", "", 1},
		{"older release", `{"tag":"0.1.145-abcdef0"}`, "", "", 1},
		{"invalid metadata", `{"tag":"../heads/master"}`, "", "invalid stable release tag", 1},
		{"archive unavailable", `{"tag":"` + tag + `"}`, archive, "download " + archive, 2},
		{"attestation unavailable", `{"tag":"` + tag + `"}`, "yagp-depoly-attestations.jsonl", "download yagp-depoly-attestations.jsonl", 3},
		{"unsigned archive", `{"tag":"` + tag + `"}`, "", "verify tool release", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A downloaded shell payload must remain data, never an update command.
			marker := filepath.Join(t.TempDir(), "executed-script")
			payload := "#!/bin/sh\ntouch '" + marker + "'\n"
			var urls []string
			client := selfUpgradeClient()
			client.Transport = selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
				urls = append(urls, req.URL.String())
				body, status := payload, http.StatusOK
				switch req.URL.String() {
				case upgrade.YAGPReleaseCache + "/latest":
					body = tc.metadata
				case base + archive, base + "yagp-depoly-attestations.jsonl":
					if tc.failed != "" && strings.HasSuffix(req.URL.Path, "/"+tc.failed) {
						status = http.StatusServiceUnavailable
					}
				default:
					t.Fatalf("native update requested a script or non-cache source: %s", req.URL)
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			executable := filepath.Join(t.TempDir(), "yagp-update")
			if err := os.WriteFile(executable, []byte("installed updater"), 0755); err != nil {
				t.Fatal(err)
			}
			err := runNativeToolUpdate(t.Context(), client, executable)
			if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want))) {
				t.Fatalf("native update returned %v, want %q", err, tc.want)
			}
			if len(urls) != tc.requests {
				t.Fatalf("requests = %v, want %d", urls, tc.requests)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("downloaded script was executed: %v", err)
			}
			if data, err := os.ReadFile(executable); err != nil || string(data) != "installed updater" {
				t.Fatalf("failed update changed installed tool: %q, %v", data, err)
			}
		})
	}
}

func TestNativeToolUpdateRejectsDevelopmentBuildBeforeNetwork(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "dev"
	t.Cleanup(func() { buildinfo.Version = previous })
	if err := runNativeToolUpdate(t.Context(), nil, ""); err == nil || !strings.Contains(err.Error(), "installed release version") {
		t.Fatalf("invalid installed version returned %v", err)
	}
	if err := runPublishedUpdate(t.Context(), deployjob.Request{Mode: "depoly"}, io.Discard); err == nil || !strings.Contains(err.Error(), "installed release version") {
		t.Fatalf("tool update did not use the native updater: %v", err)
	}
}

func TestDownloadToolReleaseKeepsArchiveAndProofAtTheSameTag(t *testing.T) {
	const tag = "0.1.147-4713edf"
	archive := "yagp-depoly-linux-" + runtime.GOARCH + ".tar.gz"
	base := upgrade.YAGPReleaseCache + "/releases/download/" + tag + "/"
	wantURLs := []string{base + archive, base + "yagp-depoly-attestations.jsonl"}
	contents := [][]byte{[]byte("archive bytes"), []byte("attestation bytes")}
	var urls []string
	client := selfUpgradeClient()
	client.Transport = selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
		i := len(urls)
		urls = append(urls, req.URL.String())
		if i >= len(contents) {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(contents[i]))}, nil
	})
	stage := t.TempDir()
	path, err := downloadToolRelease(t.Context(), client, tag, stage)
	if err != nil || path != filepath.Join(stage, archive) || !slices.Equal(urls, wantURLs) {
		t.Fatalf("download = %q, %v, requests %v", path, err, urls)
	}
	for i, name := range []string{archive, "yagp-depoly-attestations.jsonl"} {
		if got, err := os.ReadFile(filepath.Join(stage, name)); err != nil || !bytes.Equal(got, contents[i]) {
			t.Fatalf("downloaded %s = %q, %v", name, got, err)
		}
	}
}

func TestNativeToolUpdateReturnsNetworkFailure(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "0.1.146-abcdef0"
	t.Cleanup(func() { buildinfo.Version = previous })
	client := selfUpgradeClient()
	client.Transport = selfUpgradeRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	if err := runNativeToolUpdate(t.Context(), client, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("network timeout returned %v", err)
	}
}
