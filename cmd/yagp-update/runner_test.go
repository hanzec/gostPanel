package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yagp/internal/buildinfo"
	"yagp/internal/deployjob"
	"yagp/pkg/upgrade"
)

func TestSubmitUpdatePersistsJobBeforeStarting(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires a root-controlled directory")
	}
	directory, err := os.MkdirTemp("/var/lib", "yagp-update-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	request := deployjob.Request{ID: "0123456789abcdef0123456789abcdef", Mode: "depoly"}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	startError := errors.New("systemd rejected the job")
	start := func(_ context.Context, got deployjob.Request) error {
		starts++
		status, err := deployjob.ReadStatus(directory, got.ID)
		if got != request || err != nil || status.Mode != request.Mode || status.State != "queued" {
			t.Fatalf("start with request %+v and status %+v, %v", got, status, err)
		}
		return startError
	}
	if err := submitUpdate(t.Context(), bytes.NewReader(data), "compose", directory, start); err == nil || starts != 0 {
		t.Fatalf("mismatched request started a job: %v, starts %d", err, starts)
	}
	if err := submitUpdate(t.Context(), bytes.NewReader(data), request.Mode, directory, start); !errors.Is(err, startError) {
		t.Fatalf("submit error = %v", err)
	}
	status, err := deployjob.ReadStatus(directory, request.ID)
	if err != nil || status.State != "failed" || status.ExitCode == nil || *status.ExitCode == 0 {
		t.Fatalf("failed submission status = %+v, %v", status, err)
	}
	if err := submitUpdate(t.Context(), bytes.NewReader(data), request.Mode, directory, start); !errors.Is(err, os.ErrExist) || starts != 1 {
		t.Fatalf("duplicate request started a job: %v, starts %d", err, starts)
	}
}

func TestUpdateWorkerPublishesProgressAndReturnsScriptExit(t *testing.T) {
	directory := t.TempDir()
	request := deployjob.Request{ID: "0123456789abcdef0123456789abcdef", Mode: "compose", Digest: "sha256:" + strings.Repeat("a", 64)}
	dir := filepath.Join(directory, request.ID)
	if err := os.Mkdir(dir, 0750); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var exitStatus string
	runScript := func(ctx context.Context, got deployjob.Request, output io.Writer) error {
		status, err := deployjob.ReadStatus(directory, got.ID)
		if got != request || err != nil || status.State != "running" {
			t.Fatalf("script with request %+v and status %+v, %v", got, status, err)
		}
		command := exec.CommandContext(ctx, "/bin/bash", "-c", `printf 'compose output\n'; exit "$1"`, "script", exitStatus)
		command.Stdout = output
		return command.Run()
	}
	if err := runUpdate(directory, request.ID, "depoly", runScript); err == nil {
		t.Fatal("worker accepted a request for a different mode")
	}
	if err := runUpdate(directory, "../jobs", request.Mode, runScript); err == nil {
		t.Fatal("worker accepted an invalid job ID")
	}
	exitStatus = "0"
	if err := runUpdate(directory, request.ID, request.Mode, runScript); err != nil {
		t.Fatalf("worker returned %v for a successful script", err)
	}
	exitStatus = "7"
	var exit *exec.ExitError
	if err := runUpdate(directory, request.ID, request.Mode, runScript); !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("worker returned %v, want exit 7", err)
	}
	status, err := deployjob.ReadStatus(directory, request.ID)
	if err != nil || strings.Count(status.Logs, "compose output\n") != 2 ||
		!strings.Contains(status.Logs, "update finished successfully") || !strings.Contains(status.Logs, "exit status 7") {
		t.Fatalf("worker logs = %+v, %v", status, err)
	}
}

func TestCanceledUpdateIsNotSubmitted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := deployjob.Request{ID: "0123456789abcdef0123456789abcdef", Mode: "depoly"}
	if err := startUpdateUnit(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("start with canceled context returned %v", err)
	}
}

func TestManualUpdateReadsDigestOnlyForCompose(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ mode, input, digest string }{
		{"compose", digest + "\n", digest}, {"depoly", "unused", ""},
	} {
		called := false
		runScript := func(_ context.Context, request deployjob.Request, _ io.Writer) error {
			called = true
			if request.Mode != tc.mode || request.Digest != tc.digest {
				t.Fatalf("script request = %+v", request)
			}
			return nil
		}
		if err := runManualUpdate(tc.mode, strings.NewReader(tc.input), io.Discard, runScript); err != nil || !called {
			t.Fatalf("manual %s returned %v, script called %t", tc.mode, err, called)
		}
	}
	if err := runManualUpdate("compose", strings.NewReader("latest"), io.Discard, nil); err == nil {
		t.Fatal("manual compose accepted a tag in place of a digest")
	}
}

func TestUpdateRejectsUninstalledExecutable(t *testing.T) {
	for _, args := range [][]string{nil, {"compose", "latest"}, {"version"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("run(%v) returned %v", args, err)
		}
	}
	t.Setenv("YAGP_UPDATE_PHASE", "version")
	if err := run([]string{"depoly"}); err != nil {
		t.Fatalf("internal version probe returned %v", err)
	}
	t.Setenv("YAGP_UPDATE_PHASE", "")
	if err := run([]string{"depoly"}); err == nil || (!strings.Contains(err.Error(), "installed") && !strings.Contains(err.Error(), "root")) {
		t.Fatalf("uninstalled updater returned %v", err)
	}
}

func TestSudoCallerCannotSelectAnInternalUpdatePhase(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	previous := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = previous }()
	t.Setenv("SUDO_UID", "1234")
	t.Setenv("SUDO_USER", "yagp-deploy")
	t.Setenv("YAGP_UPDATE_JOB", "invalid")
	for _, phase := range []string{"worker", "finish", "apply"} {
		if err := runInstalledUpdate(deployjob.Executable, "depoly", phase); err == nil || err.Error() != "invalid update request" {
			t.Fatalf("sudo caller selected %s: %v", phase, err)
		}
	}
	t.Setenv("SUDO_USER", "operator")
	if err := runInstalledUpdate(deployjob.Executable, "compose", "worker"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("operator selected a worker phase: %v", err)
	}
	t.Setenv("SUDO_UID", "")
	for _, phase := range []string{"worker", "finish"} {
		if err := runInstalledUpdate(deployjob.Executable, "depoly", phase); err == nil || err.Error() != "invalid update job ID" {
			t.Fatalf("systemd %s with invalid job ID: %v", phase, err)
		}
	}
	t.Setenv("YAGP_UPDATE_RELEASE_TAG", "invalid")
	if err := runInstalledUpdate(deployjob.Executable, "depoly", "apply"); err == nil || !strings.Contains(err.Error(), "release versions") {
		t.Fatalf("apply with invalid version returned %v", err)
	}
	if err := runInstalledUpdate(deployjob.Executable, "compose", "apply"); err == nil || err.Error() != "invalid update phase" {
		t.Fatalf("compose entered the tool replacement phase: %v", err)
	}
}

func TestApplyToolReleaseNeverDowngradesAndRequiresInstalledPair(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "0.1.90-abcdef0"
	t.Cleanup(func() { buildinfo.Version = previous })
	executable := filepath.Join(t.TempDir(), "yagp-update")
	for _, tag := range []string{"0.1.89-abcdef0", buildinfo.Version} {
		if err := applyToolRelease(t.Context(), executable, tag); err != nil {
			t.Fatalf("skip tool update for %s: %v", tag, err)
		}
	}
	if err := applyToolRelease(t.Context(), executable, "0.1.91-abcdef0"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("update without installed receiver returned %v", err)
	}
}

func TestRunReleaseScriptRequiresAttestation(t *testing.T) {
	const tag = "0.1.90-abcdef0"
	for _, tc := range []struct {
		name     string
		verified bool
		code     int
	}{
		{name: "verified", verified: true},
		{name: "unverified"},
		{name: "failed script", verified: true, code: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := filepath.Join(t.TempDir(), "gh")
			verify := "exit 1"
			if tc.verified {
				verify = `test "$1" = attestation && test "$2" = verify && test -s "$3" && test -s "$5"`
			}
			ghScript := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'gh version 2.102.0 (2026-10-02)'; exit 0; fi\n" + verify + "\n"
			if err := os.WriteFile(gh, []byte(ghScript), 0700); err != nil {
				t.Fatal(err)
			}
			script := `read -r digest
printf '%s %s %s\n' "$1" "$YAGP_UPDATE_RELEASE_TAG" "$digest"
`
			if tc.code != 0 {
				script += "exit 7\n"
			}
			client := selfUpgradeClient()
			client.Transport = selfUpgradeRoundTripper(func(req *http.Request) (*http.Response, error) {
				var body string
				switch req.URL.String() {
				case upgrade.YAGPReleaseCache + "/latest":
					if req.Header.Get("Accept") != "application/json" {
						t.Error("release request did not select JSON")
					}
					body = `{"tag":"` + tag + `"}`
				case upgrade.YAGPReleaseCache + "/releases/download/" + tag + "/setup_deploy.sh":
					body = script
				case upgrade.YAGPReleaseCache + "/releases/download/" + tag + "/" + selfUpgradeBundle:
					body = "test attestation"
				default:
					t.Errorf("unexpected request to %s", req.URL)
					return nil, errors.New("unexpected release request")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			digest := "sha256:" + strings.Repeat("a", 64)
			var output bytes.Buffer
			err := runReleaseScript(t.Context(), client, gh, deployjob.Request{Mode: "compose", Digest: digest}, &output)
			if !tc.verified {
				if err == nil || output.Len() != 0 {
					t.Fatalf("unverified script returned %v, output %q", err, output.String())
				}
				return
			}
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
					t.Fatalf("script returned %v, want exit %d", err, tc.code)
				}
			}
			if got, want := output.String(), "compose "+tag+" "+digest+"\n"; got != want {
				t.Fatalf("script output %q, want %q", got, want)
			}
		})
	}
}

func TestFinishUpdateRecordsProcessResult(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name, result, kind, exit, state string
		code                            int
	}{
		{"success", "success", "exited", "0", "succeeded", 0},
		{"exit failure", "exit-code", "exited", "7", "failed", 7},
		{"terminated", "timeout", "killed", "TERM", "failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SERVICE_RESULT", tc.result)
			t.Setenv("EXIT_CODE", tc.kind)
			t.Setenv("EXIT_STATUS", tc.exit)
			directory := t.TempDir()
			dir := filepath.Join(directory, id)
			if err := os.Mkdir(dir, 0750); err != nil {
				t.Fatal(err)
			}
			if err := writeStatus(dir, deployjob.Status{Mode: "depoly", State: "running"}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "job.log"), []byte("updating\n"), 0640); err != nil {
				t.Fatal(err)
			}
			if err := finishUpdate(directory, id, "depoly"); err != nil {
				t.Fatal(err)
			}
			status, err := deployjob.ReadStatus(directory, id)
			if err != nil || status.Mode != "depoly" || status.State != tc.state ||
				status.ExitCode == nil || *status.ExitCode != tc.code || status.Logs != "updating\n" {
				t.Fatalf("status = %+v, %v", status, err)
			}
		})
	}
}
