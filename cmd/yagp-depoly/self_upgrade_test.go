package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type selfUpgradeRoundTripper func(*http.Request) (*http.Response, error)

func (transport selfUpgradeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestLatestDeployRelease(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"stable", `{"tag_name":"0.1.132-aabbccd"}`, "0.1.132-aabbccd"},
		{"draft", `{"tag_name":"0.1.132-aabbccd","draft":true}`, ""},
		{"prerelease", `{"tag_name":"0.1.132-aabbccd","prerelease":true}`, ""},
		{"invalid tag", `{"tag_name":"../0.1.132-aabbccd"}`, ""},
		{"broken JSON", `{"tag_name":`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://api.github.com/repos/hanzec/yagp/releases/latest" {
					t.Fatalf("unexpected request %s", req.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			got, err := latestDeployRelease(context.Background(), client)
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("latestDeployRelease = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestDownloadSelfUpgradeFile(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		content string
		wantErr bool
	}{
		{"valid", 200, "1234", false},
		{"empty", 200, "", true},
		{"oversized", 200, "12345", true},
		{"missing asset", 404, "not found", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Status: http.StatusText(tt.status), Body: io.NopCloser(strings.NewReader(tt.content))}, nil
			})}
			target := filepath.Join(t.TempDir(), "download")
			err := downloadSelfUpgradeFile(context.Background(), client, "https://github.com/asset", target, 4)
			if (err != nil) != tt.wantErr {
				t.Fatalf("download = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != tt.content {
					t.Fatalf("downloaded file = %q, %v", data, err)
				}
			}
		})
	}
}

func TestSelfUpgradeVerificationCommands(t *testing.T) {
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte(`#!/bin/sh
case "$1" in
--version)
  echo 'gh version 2.102.0 (2026-10-02)'
  ;;
attestation)
  printf '%s\n' "$@" > args
  test "$4" = '--bundle' && test -f "$5"
  ;;
esac
`), 0755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := verifySelfUpgradeGHVersion(ctx, gh, dir); err != nil {
		t.Fatal(err)
	}
	archive, bundle := filepath.Join(dir, "binary.tar.gz"), filepath.Join(dir, "bundle.jsonl")
	if err := verifySelfUpgradeAttestation(ctx, gh, dir, archive, bundle); err == nil {
		t.Fatal("failed verification was accepted")
	}
	if err := os.WriteFile(bundle, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySelfUpgradeAttestation(ctx, gh, dir, archive, bundle); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"attestation", "verify", archive, "--bundle", bundle,
		"--repo", "hanzec/yagp", "--signer-workflow", "hanzec/yagp/.github/workflows/release.yml",
		"--source-ref", "refs/heads/master", "--deny-self-hosted-runners"}
	if string(got) != strings.Join(want, "\n")+"\n" {
		t.Fatalf("verification arguments = %q, want %q", got, want)
	}

	if err := os.WriteFile(gh, []byte("#!/bin/sh\necho 'gh version 2.101.9'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := verifySelfUpgradeGHVersion(ctx, gh, dir); err == nil {
		t.Fatal("GitHub CLI with a known policy bypass was accepted")
	}
}

func TestExtractSelfUpgradeBinary(t *testing.T) {
	const name = "yagp-depoly-linux-amd64"
	for _, tt := range []struct {
		name    string
		entries []tar.Header
		wantErr bool
	}{
		{"regular", []tar.Header{{Name: "LICENSE", Size: 3}, {Name: name, Size: 4}}, false},
		{"wrong architecture", []tar.Header{{Name: "yagp-depoly-linux-arm64", Size: 4}}, true},
		{"symlink", []tar.Header{{Name: name, Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}}, true},
		{"duplicate", []tar.Header{{Name: name, Size: 4}, {Name: name, Size: 4}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "tool.tar.gz")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			compressed := gzip.NewWriter(file)
			writer := tar.NewWriter(compressed)
			for _, header := range tt.entries {
				if err := writer.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(writer, strings.Repeat("x", int(header.Size))); err != nil {
					t.Fatal(err)
				}
			}
			if err := errors.Join(writer.Close(), compressed.Close(), file.Close()); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "candidate")
			err = extractSelfUpgradeBinary(archive, name, target)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extractSelfUpgradeBinary = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "xxxx" {
					t.Fatalf("candidate = %q, %v", data, err)
				}
			}
		})
	}
}

func TestReplaceSelfUpgradeBinary(t *testing.T) {
	for _, tt := range []struct {
		name            string
		failRestart     bool
		sameInodeBackup bool
	}{
		{name: "success"},
		{name: "rollback", failRestart: true},
		{name: "retry after rollback", failRestart: true, sameInodeBackup: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stage, err := os.MkdirTemp(dir, "stage")
			if err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(dir, "yagp-depoly")
			candidate := filepath.Join(stage, "candidate")
			previous := filepath.Join(dir, ".yagp-depoly.previous")
			for path, version := range map[string]string{executable: "old", candidate: "new"} {
				if err := os.WriteFile(path, []byte(version), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.sameInodeBackup {
				if err := os.Link(executable, previous); err != nil {
					t.Fatal(err)
				}
			}

			calls := 0
			restartErr := errors.New("readiness failed")
			err = replaceSelfUpgradeBinary(executable, candidate, func(rollback bool) error {
				calls++
				if rollback != (calls == 2) {
					t.Fatalf("restart call %d received rollback=%t", calls, rollback)
				}
				want := "new"
				if calls == 2 {
					want = "old"
				}
				if live, err := os.ReadFile(executable); err != nil || string(live) != want {
					t.Fatalf("running tool = %q, %v; want %q", live, err, want)
				}
				if tt.failRestart && calls == 1 {
					return restartErr
				}
				return nil
			})
			if tt.failRestart {
				if !errors.Is(err, restartErr) || calls != 2 {
					t.Fatalf("rollback = %v, restart calls = %d", err, calls)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("upgrade = %v, restart calls = %d", err, calls)
			}
			if backup, err := os.ReadFile(previous); err != nil || string(backup) != "old" {
				t.Fatalf("backup = %q, %v", backup, err)
			}
		})
	}
}

func TestInstallSelfUpgradeRelease(t *testing.T) {
	const current, newer = "0.1.131-aabbccd", "0.1.132-bbccdde"
	versionScript := func(tag string) string {
		return "#!/bin/sh\ntest \"$1\" = version || exit 2\nprintf '%s\\n' '" + tag + "'\n"
	}
	name := "yagp-depoly-linux-" + runtime.GOARCH
	for _, tt := range []struct {
		name, tag, candidate, failure, want string
	}{
		{"new version", newer, newer, "", newer},
		{"rejected attestation", newer, newer, "verify", current},
		{"missing archive", newer, newer, "archive", current},
		{"missing bundle", newer, newer, "bundle", current},
		{"malformed archive", newer, newer, "extract", current},
		{"mislabeled binary", newer, current, "version", current},
		{"changed installed version", newer, newer, "changed", newer},
		{"failed readiness", newer, newer, "restart", current},
		{"unhealthy after startup", newer, newer, "is-active", current},
		{"same version", current, current, "", current},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, content string) string {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
				return path
			}
			installed := current
			if tt.failure == "changed" {
				installed = newer
			}
			executable := write("yagp-depoly", versionScript(installed))
			gh := write("gh", `#!/bin/sh
case "$1" in
--version) echo 'gh version 2.102.0 (2026-10-02)';;
attestation) test -f "$3" && test -f "$5" && test ! -f "$(dirname "$0")/verify";;
*) exit 2;;
esac
`)
			systemctl := write("systemctl", `#!/bin/sh
dir=$(dirname "$0")
printf '%s\n' "$*" >> "$dir/systemctl.log"
case "$1" in
restart)
  if test -f "$dir/restart"; then rm "$dir/restart"; exit 2; fi
  ;;
is-active)
  if test -f "$dir/is-active"; then rm "$dir/is-active"; exit 2; fi
  ;;
reset-failed) ;;
*) exit 2;;
esac
`)
			if tt.failure == "verify" || tt.failure == "restart" || tt.failure == "is-active" {
				write(tt.failure, "")
			}
			var archive bytes.Buffer
			compressed := gzip.NewWriter(&archive)
			writer := tar.NewWriter(compressed)
			binary := versionScript(tt.candidate)
			if err := writer.WriteHeader(&tar.Header{Name: name, Size: int64(len(binary))}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(writer, binary); err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(writer.Close(), compressed.Close()); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
				var body io.Reader
				status := 200
				switch req.URL.String() {
				case "https://api.github.com/repos/hanzec/yagp/releases/latest":
					body = strings.NewReader(`{"tag_name":"` + tt.tag + `"}`)
				case "https://github.com/hanzec/yagp/releases/download/" + tt.tag + "/" + name + ".tar.gz":
					body = bytes.NewReader(archive.Bytes())
					if tt.failure == "archive" {
						status = 404
					}
					if tt.failure == "extract" {
						body = strings.NewReader("not an archive")
					}
				case "https://github.com/hanzec/yagp/releases/download/" + tt.tag + "/yagp-depoly-attestations.jsonl":
					body = strings.NewReader("signed fixture")
					if tt.failure == "bundle" {
						status = 404
					}
				default:
					t.Fatalf("unexpected request %s", req.URL)
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(body)}, nil
			})}
			env := selfUpgradeEnvironment{
				client: client, gh: gh, systemctl: systemctl, lockPath: filepath.Join(dir, "deploy.lock"),
			}
			err := env.install(context.Background(), executable, current)
			if (err != nil) != (tt.failure != "") {
				t.Fatalf("install = %v, want failure %q", err, tt.failure)
			}
			if live, err := os.ReadFile(executable); err != nil || string(live) != versionScript(tt.want) {
				t.Fatalf("installed executable = %q, %v; want %s", live, err, tt.want)
			}
			log, err := os.ReadFile(filepath.Join(dir, "systemctl.log"))
			if tt.failure == "restart" || tt.failure == "is-active" {
				if err != nil || !strings.Contains(string(log), "reset-failed yagp-depoly.service yagp-depoly-upgrade-webhook.service") {
					t.Fatalf("rollback did not reset/restart systemd units: %q, %v", log, err)
				}
			} else if tt.failure == "" && tt.want == newer {
				if err != nil || !strings.Contains(string(log), "is-active --quiet yagp-depoly-upgrade-webhook.service") {
					t.Fatalf("tool did not check both services: %q, %v", log, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("services restarted without an installed release: %q, %v", log, err)
			}
		})
	}
}

func TestSelfUpgradeLockDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.lock")
	lock, err := acquireSelfUpgradeLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second, err := acquireSelfUpgradeLock(ctx, path)
	if second != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock = %v, %v; want deadline error", second, err)
	}
}

func TestSelfUpgradeTrustedToolsAndRedirects(t *testing.T) {
	if _, err := findSelfUpgradeTool("sh"); err != nil {
		t.Fatal(err)
	}
	if _, err := findSelfUpgradeTool("this-yagp-tool-does-not-exist"); err == nil {
		t.Fatal("accepted missing tool")
	}
	if err := checkSelfUpgradeFile("relative"); err == nil {
		t.Fatal("accepted relative tool path")
	}
	origin, err := http.NewRequest("GET", "https://github.com/hanzec/yagp/releases/download/1/asset", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://release-assets.githubusercontent.com/asset", "http://github.com/asset", "https://elsewhere.example/asset"} {
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = selfUpgradeClient().CheckRedirect(req, []*http.Request{origin})
		if (err == nil) != strings.Contains(target, "githubusercontent.com") {
			t.Errorf("redirect to %s = %v", target, err)
		}
	}
}
