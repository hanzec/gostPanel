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
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yagp/pkg/upgrade"
)

type selfUpgradeRoundTripper func(*http.Request) (*http.Response, error)

func (transport selfUpgradeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestSelfUpgradePreflight(t *testing.T) {
	tool, err := findSelfUpgradeTool("sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, unavailable, uid, gid, want string
	}{
		{"success", "", "65534", "65534", ""},
		{"missing executable", "executable", "65534", "65534", "no such file"},
		{"unsafe lock directory", "lock", "65534", "65534", ""},
		{"missing gh", "gh", "65534", "65534", "gh unavailable"},
		{"missing systemctl", "systemctl", "65534", "65534", "systemctl unavailable"},
		{"missing account", "account", "65534", "65534", "find unprivileged tool account"},
		{"root account", "", "0", "65534", "invalid unprivileged tool account"},
		{"invalid uid", "", "-1", "65534", "invalid unprivileged tool account"},
		{"root group", "", "65534", "0", "invalid unprivileged tool account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executable, lock := tool, filepath.Join(filepath.Dir(tool), "deploy.lock")
			want := tt.want
			switch tt.unavailable {
			case "executable":
				executable = filepath.Join(t.TempDir(), "missing")
			case "lock":
				unsafeDir := filepath.Join(t.TempDir(), "world-writable")
				if err := os.Mkdir(unsafeDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(unsafeDir, 0777); err != nil {
					t.Fatal(err)
				}
				lock = filepath.Join(unsafeDir, "deploy.lock")
				want = "tool path is not root-controlled"
			}
			env, err := prepareSelfUpgrade(executable, lock, func(name string) (string, error) {
				if name == tt.unavailable {
					return "", errors.New(name + " unavailable")
				}
				return tool, nil
			}, func(name string) (*user.User, error) {
				if name != "yagp-deploy" {
					t.Fatalf("unexpected account lookup %s", name)
				}
				if tt.unavailable == "account" {
					return nil, user.UnknownUserError(name)
				}
				return &user.User{Uid: tt.uid, Gid: tt.gid}, nil
			})
			if want != "" {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("prepareSelfUpgrade = %v; want %q", err, want)
				}
				return
			}
			if err != nil || env.client == nil || env.lockPath != lock || env.gh != tool || env.systemctl != tool || env.credentials == nil ||
				env.credentials.Uid != 65534 || env.credentials.Gid != 65534 || len(env.credentials.Groups) != 0 {
				t.Fatalf("unexpected self-upgrade environment: %+v, %v", env, err)
			}
		})
	}
}

func TestSelfUpgradeNetworkFailure(t *testing.T) {
	networkErr := errors.New("network unavailable")
	client := &http.Client{Transport: selfUpgradeRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, networkErr
	})}
	if _, err := getSelfUpgradeFile(context.Background(), client, "https://github.com/asset"); !errors.Is(err, networkErr) {
		t.Fatalf("download error = %v, want %v", err, networkErr)
	}
	if _, err := getSelfUpgradeFile(context.Background(), client, "://malformed"); err == nil {
		t.Fatal("malformed release URL accepted")
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
			if tt.wantErr {
				if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed download left a file or returned a stat error: %v", err)
				}
				return
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != tt.content {
				t.Fatalf("downloaded file = %q, %v", data, err)
			}
		})
	}
}

func TestDownloadKeepsExistingFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "download")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: selfUpgradeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("new"))}, nil
	})}
	if err := downloadSelfUpgradeFile(context.Background(), client, "https://github.com/asset", target, 4); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite download = %v, want existing-file error", err)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "old" {
		t.Fatalf("existing file = %q, %v", contents, err)
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
		name     string
		entries  []tar.Header
		existing bool
		wantErr  bool
	}{
		{"regular", []tar.Header{{Name: "LICENSE", Size: 3}, {Name: name, Size: 4}}, false, false},
		{"wrong architecture", []tar.Header{{Name: "yagp-depoly-linux-arm64", Size: 4}}, false, true},
		{"symlink", []tar.Header{{Name: name, Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}}, false, true},
		{"duplicate", []tar.Header{{Name: name, Size: 4}, {Name: name, Size: 4}}, false, true},
		{"existing candidate", []tar.Header{{Name: name, Size: 4}}, true, true},
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
			if tt.existing {
				if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			err = extractSelfUpgradeBinaries(archive, map[string]string{name: target})
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

func TestExtractMissingOrMalformedTar(t *testing.T) {
	dir := t.TempDir()
	archive, target := filepath.Join(dir, "archive"), filepath.Join(dir, "binary")
	if err := extractSelfUpgradeBinaries(archive, map[string]string{"binary": target}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing archive = %v, want file error", err)
	}
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	_, err = io.WriteString(writer, strings.Repeat("x", 512))
	if err := errors.Join(err, writer.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	if err := extractSelfUpgradeBinaries(archive, map[string]string{"binary": target}); !errors.Is(err, tar.ErrHeader) {
		t.Fatalf("malformed archive = %v, want malformed tar error", err)
	}
}

func TestExtractTruncatedBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: "binary", Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "x"); err != nil {
		t.Fatal(err)
	}
	// Close the compressed stream without writing the missing tar contents.
	if err := errors.Join(compressed.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	if err := extractSelfUpgradeBinaries(archive, map[string]string{"binary": filepath.Join(dir, "candidate")}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated archive = %v, want unexpected EOF", err)
	}
}

func TestSelfUpgradeVersionCannotExecuteMissingTool(t *testing.T) {
	if err := checkSelfUpgradeVersion(context.Background(), filepath.Join(t.TempDir(), "missing"), "0.1.132-aabbccd", nil, false); err == nil {
		t.Fatal("version check accepted missing executable")
	}
}

func TestReplaceSelfUpgradeBinaries(t *testing.T) {
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
			binaries := []binaryReplacement{
				{target: filepath.Join(dir, "yagp-update"), candidate: filepath.Join(stage, "update")},
				{target: filepath.Join(dir, "yagp-depoly"), candidate: filepath.Join(stage, "depoly")},
			}
			for _, binary := range binaries {
				name := filepath.Base(binary.target)
				for path, version := range map[string]string{binary.target: "old " + name, binary.candidate: "new " + name} {
					if err := os.WriteFile(path, []byte(version), 0755); err != nil {
						t.Fatal(err)
					}
				}
				if tt.sameInodeBackup {
					if err := os.Link(binary.target, binary.previous()); err != nil {
						t.Fatal(err)
					}
				}
			}

			calls := 0
			restartErr := errors.New("readiness failed")
			err = replaceSelfUpgradeBinaries(binaries, func(rollback bool) error {
				calls++
				if rollback != (calls == 2) {
					t.Fatalf("restart call %d received rollback=%t", calls, rollback)
				}
				for _, binary := range binaries {
					want := "new " + filepath.Base(binary.target)
					if rollback {
						want = "old " + filepath.Base(binary.target)
					}
					if live, err := os.ReadFile(binary.target); err != nil || string(live) != want {
						t.Fatalf("installed tool = %q, %v; want %q", live, err, want)
					}
				}
				if tt.failRestart && !rollback {
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
			for _, binary := range binaries {
				if backup, err := os.ReadFile(binary.previous()); err != nil || string(backup) != "old "+filepath.Base(binary.target) {
					t.Fatalf("backup = %q, %v", backup, err)
				}
			}
		})
	}
}

func TestReplaceSelfUpgradeBinariesRestoresAfterPartialInstall(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	updater := binaryReplacement{filepath.Join(dir, "yagp-update"), filepath.Join(stage, "new-updater")}
	receiver := binaryReplacement{filepath.Join(dir, "yagp-depoly"), filepath.Join(stage, "missing-receiver")}
	for path, content := range map[string]string{updater.target: "old updater", updater.candidate: "new updater", receiver.target: "old receiver"} {
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	err := replaceSelfUpgradeBinaries([]binaryReplacement{updater, receiver}, func(bool) error {
		t.Fatal("restarted service after only one binary could be installed")
		return nil
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upgrade = %v, want missing candidate error", err)
	}
	for path, want := range map[string]string{updater.target: "old updater", receiver.target: "old receiver"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatalf("binary %s = %q, %v; want %q", path, data, err, want)
		}
	}
}

func TestSelfUpgradeFileFailures(t *testing.T) {
	for _, tt := range []struct {
		name, wantError string
		wantRestarts    int
	}{
		{"missing installed tool", "back up installed tool", 0},
		{"previous path is a directory", "save previous tool version", 0},
		{"missing replacement", "replace installed tool", 0},
		{"cannot stage rollback", "stage tool rollback", 1},
		{"cannot restore rollback", "restore previous tool", 1},
		{"cannot restart rollback", "restart previous tool", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stage := filepath.Join(dir, "stage")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.Mkdir(stage, 0700))
			executable := filepath.Join(dir, "yagp-depoly")
			previous := filepath.Join(dir, ".yagp-depoly.previous")
			candidate := filepath.Join(stage, "candidate")
			must(os.WriteFile(executable, []byte("old"), 0755))
			must(os.WriteFile(candidate, []byte("new"), 0755))
			switch tt.name {
			case "missing installed tool":
				must(os.Remove(executable))
			case "previous path is a directory":
				must(os.Mkdir(previous, 0700))
			case "missing replacement":
				must(os.Remove(candidate))
			}

			restarts := 0
			err := replaceSelfUpgradeBinaries([]binaryReplacement{{executable, candidate}}, func(rollback bool) error {
				restarts++
				if !rollback {
					switch tt.name {
					case "cannot stage rollback":
						must(os.Mkdir(filepath.Join(stage, filepath.Base(executable)+".rollback"), 0700))
					case "cannot restore rollback":
						must(os.Remove(executable))
						must(os.Mkdir(executable, 0700))
					}
				}
				return errors.New("not ready")
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) || restarts != tt.wantRestarts {
				t.Fatalf("replacement = %v after %d restarts; want %q after %d restarts", err, restarts, tt.wantError, tt.wantRestarts)
			}
			if restarts > 0 {
				backup, err := os.ReadFile(previous)
				if err != nil || string(backup) != "old" {
					t.Fatalf("failed upgrade lost previous version: %q, %v", backup, err)
				}
			}
		})
	}
}

func TestInstallSelfUpgradeRelease(t *testing.T) {
	const current, newer = "0.1.131-aabbccd", "0.1.132-bbccdde"
	versionScript := func(tag string, updater bool) string {
		check := "test \"$1\" = version"
		if updater {
			check = "test \"$1\" = depoly && test \"$YAGP_UPDATE_PHASE\" = version"
		}
		return "#!/bin/sh\n" + check + " || exit 2\nprintf '%s\\n' '" + tag + "'\n"
	}
	receiverName := "yagp-depoly-linux-" + runtime.GOARCH
	updaterName := "yagp-update-linux-" + runtime.GOARCH
	for _, failure := range []string{
		"", "verify", "archive", "bundle", "extract", "missing-updater", "stage", "gh", "receiver-version",
		"updater-version", "changed", "lock", "restart", "is-active",
	} {
		t.Run("cache-"+failure, func(t *testing.T) {
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
			if failure == "changed" {
				installed = newer
			}
			executable := write("yagp-update", versionScript(installed, true))
			receiver := write("yagp-depoly", versionScript(installed, false))
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
			if failure == "verify" || failure == "restart" || failure == "is-active" {
				write(failure, "")
			}
			var archive bytes.Buffer
			compressed := gzip.NewWriter(&archive)
			writer := tar.NewWriter(compressed)
			entries := []struct {
				name    string
				updater bool
			}{{receiverName, false}, {updaterName, true}}
			for _, entry := range entries {
				if failure == "missing-updater" && entry.updater {
					continue
				}
				candidate := newer
				if (failure == "receiver-version" && !entry.updater) || (failure == "updater-version" && entry.updater) {
					candidate = current
				}
				binary := versionScript(candidate, entry.updater)
				if err := writer.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(binary))}); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(writer, binary); err != nil {
					t.Fatal(err)
				}
			}
			if err := errors.Join(writer.Close(), compressed.Close()); err != nil {
				t.Fatal(err)
			}
			cacheAssets := upgrade.YAGPReleaseCache + "/releases/download/" + newer + "/"
			client := &http.Client{Transport: selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
				var body io.Reader
				status := 200
				switch req.URL.String() {
				case cacheAssets + receiverName + ".tar.gz":
					body = bytes.NewReader(archive.Bytes())
					if failure == "archive" {
						status = 404
					}
					if failure == "extract" {
						body = strings.NewReader("not an archive")
					}
				case cacheAssets + selfUpgradeBundle:
					body = strings.NewReader("signed fixture")
					if failure == "bundle" {
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
			target := executable
			switch failure {
			case "stage":
				target = filepath.Join(dir, "missing", "yagp-update")
			case "gh":
				env.gh = filepath.Join(dir, "missing-gh")
			case "lock":
				if err := os.Symlink(executable, env.lockPath); err != nil {
					t.Fatal(err)
				}
			}
			err := env.install(context.Background(), target, receiver, current, newer)
			if (err != nil) != (failure != "") {
				t.Fatalf("install = %v, want failure %q", err, failure)
			}
			want := installed
			if failure == "" {
				want = newer
			}
			for path, updater := range map[string]bool{executable: true, receiver: false} {
				if live, err := os.ReadFile(path); err != nil || string(live) != versionScript(want, updater) {
					t.Fatalf("installed executable %s = %q, %v; want %s", path, live, err, want)
				}
			}
			log, err := os.ReadFile(filepath.Join(dir, "systemctl.log"))
			const restart = "restart yagp-depoly.service\n"
			const active = "is-active --quiet yagp-depoly.service\n"
			const reset = "reset-failed yagp-depoly.service\n"
			var wantLog string
			switch failure {
			case "restart":
				wantLog = restart + reset + restart + active
			case "is-active":
				wantLog = restart + active + reset + restart + active
			case "":
				wantLog = restart + active
			}
			if wantLog == "" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("service restarted without an installed release: %q, %v", log, err)
				}
			} else if err != nil || string(log) != wantLog {
				t.Fatalf("systemctl commands = %q, %v; want %q", log, err, wantLog)
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

func TestSelfUpgradeLockRejectsWritableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploy.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireSelfUpgradeLock(context.Background(), path)
	if lock != nil || err == nil || !strings.Contains(err.Error(), "not root-controlled") {
		t.Fatalf("unsafe lock = %v, %v", lock, err)
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
	origin, err := http.NewRequest("GET", upgrade.YAGPReleaseCache+"/releases/download/1/asset", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{upgrade.YAGPReleaseCache + "/releases/download/1/another-asset", "http://" + origin.URL.Host + "/asset", "https://elsewhere.example/asset"} {
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = selfUpgradeClient().CheckRedirect(req, []*http.Request{origin})
		if (err == nil) != strings.HasPrefix(target, upgrade.YAGPReleaseCache) {
			t.Errorf("redirect to %s = %v", target, err)
		}
	}
}
