package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"yagp/internal/buildinfo"
)

func TestCLIEntry(t *testing.T) {
	if err := run(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("run with no arguments: %v", err)
	}
	if err := run([]string{"version", "unexpected"}); err == nil || !strings.Contains(err.Error(), "invalid yagp-depoly command") {
		t.Fatalf("run with unexpected version arguments: %v", err)
	}
	if err := run([]string{"serve"}); err == nil || !strings.Contains(err.Error(), "set -listen") {
		t.Fatalf("run serve = %v, want webhook flag validation", err)
	}

	oldArgs := os.Args
	os.Args = []string{"yagp-depoly", "version"}
	defer func() { os.Args = oldArgs }()
	stdout, stderr, _ := captureCLI(t, "", func() error {
		main()
		return nil
	})
	if stdout != buildinfo.Version+"\n" || stderr != "" {
		t.Fatalf("version stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestPrivilegedDispatch(t *testing.T) {
	const download = "/tmp/yagp-depoly-linux-amd64"
	cases := []struct {
		name string
		exe  string
		root bool
		args []string
		want []string
	}{
		{name: "deploy from installed path", exe: installedExecutable, root: true, args: []string{"deploy"}, want: []string{}},
		{name: "upgrade from installed path", exe: installedExecutable, root: true, args: []string{"self-upgrade"}, want: []string{"self-upgrade"}},
		{name: "install from download", exe: download, root: true, args: []string{"install", "/opt/yagp"}, want: []string{"install-webhook", "/opt/yagp", download}},
		{name: "install with empty positional proxy uses default", exe: download, root: true, args: []string{"install", "/opt/yagp", ""}, want: []string{"install-webhook", "/opt/yagp", download}},
		{name: "install with proxy name", exe: download, root: true, args: []string{"install", "/opt/yagp", "edge"}, want: []string{"install-webhook", "/opt/yagp", download, "edge"}},
		{name: "install with named proxy", exe: download, root: true, args: []string{"install", "/opt/yagp", "--traefik", "edge"}, want: []string{"install-webhook", "/opt/yagp", download, "edge"}},
		{name: "install with proxy ID assignment", exe: download, root: true, args: []string{"install", "/opt/yagp", "--traefik=54c2906c14d1"}, want: []string{"install-webhook", "/opt/yagp", download, "54c2906c14d1"}},
		{name: "install upgrade default proxy", exe: installedExecutable, root: true, args: []string{"install-upgrade"}, want: []string{"install-upgrade"}},
		{name: "install upgrade with proxy name", exe: installedExecutable, root: true, args: []string{"install-upgrade", "edge"}, want: []string{"install-upgrade", "edge"}},
		{name: "install upgrade with named proxy ID", exe: installedExecutable, root: true, args: []string{"install-upgrade", "--traefik", "54c2906c14d1"}, want: []string{"install-upgrade", "54c2906c14d1"}},
		{name: "install upgrade with proxy assignment", exe: installedExecutable, root: true, args: []string{"install-upgrade", "--traefik=edge"}, want: []string{"install-upgrade", "edge"}},
		{name: "non-root deploy", exe: installedExecutable, args: []string{"deploy"}},
		{name: "non-root upgrade", exe: installedExecutable, args: []string{"self-upgrade"}},
		{name: "deploy from download", exe: download, root: true, args: []string{"deploy"}},
		{name: "upgrade from download", exe: download, root: true, args: []string{"self-upgrade"}},
		{name: "extra deploy argument", exe: installedExecutable, root: true, args: []string{"deploy", "/tmp/other"}},
		{name: "extra upgrade argument", exe: installedExecutable, root: true, args: []string{"self-upgrade", "/tmp/other"}},
		{name: "non-root install", exe: download, args: []string{"install", "/opt/yagp"}},
		{name: "missing install project", exe: download, root: true, args: []string{"install"}},
		{name: "install proxy flag without project", exe: download, root: true, args: []string{"install", "--traefik", "edge"}},
		{name: "extra install argument", exe: download, root: true, args: []string{"install", "/opt/yagp", "edge", "other"}},
		{name: "missing install proxy value", exe: download, root: true, args: []string{"install", "/opt/yagp", "--traefik"}},
		{name: "empty install proxy assignment", exe: download, root: true, args: []string{"install", "/opt/yagp", "--traefik="}},
		{name: "unknown install proxy flag", exe: download, root: true, args: []string{"install", "/opt/yagp", "--other=edge"}},
		{name: "non-root install upgrade", exe: installedExecutable, args: []string{"install-upgrade"}},
		{name: "extra install upgrade argument", exe: installedExecutable, root: true, args: []string{"install-upgrade", "edge", "other"}},
		{name: "duplicate install upgrade proxy flags", exe: installedExecutable, root: true, args: []string{"install-upgrade", "--traefik=edge", "--traefik=other"}},
		{name: "mixed install upgrade proxy selectors", exe: installedExecutable, root: true, args: []string{"install-upgrade", "edge", "--traefik=other"}},
		{name: "install upgrade proxy flag used as value", exe: installedExecutable, root: true, args: []string{"install-upgrade", "--traefik", "--traefik=edge"}},
	}
	commandErr := errors.New("privileged command result")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := dispatch(tc.args, tc.exe, tc.root, func(exe string, args ...string) error {
				called = true
				if exe != tc.exe || !slices.Equal(args, tc.want) {
					t.Errorf("executor got (%q, %q), want (%q, %q)", exe, args, tc.exe, tc.want)
				}
				return commandErr
			})
			if tc.want == nil {
				if called || err == nil {
					t.Fatalf("invalid command reached executor: called = %t, err = %v", called, err)
				}
			} else if !called || !errors.Is(err, commandErr) {
				t.Fatalf("executor result not propagated: called = %t, err = %v", called, err)
			}
		})
	}
}

func TestEmbeddedInstallerValidation(t *testing.T) {
	startup := filepath.Join(t.TempDir(), "bash-startup")
	if err := os.WriteFile(startup, []byte("echo unexpected-startup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASH_ENV", startup)

	stdout, stderr, err := captureCLI(t, "invalid-digest\n", func() error {
		return executePrivileged(installedExecutable)
	})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("embedded installer exit = %v, want exit code 1", err)
	}
	want := "请在 Linux 服务器上以 root 执行"
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		want = "需要完整的小写 SHA-256 镜像 digest"
	}
	if stdout != "" || !strings.Contains(stderr, want) {
		t.Fatalf("embedded installer stdout = %q, stderr = %q, want error containing %q", stdout, stderr, want)
	}
}

func TestExecutePrivilegedUpgradeDispatch(t *testing.T) {
	err := executePrivileged(filepath.Join(t.TempDir(), "missing-tool"), "self-upgrade")
	var exit *exec.ExitError
	if err == nil || errors.As(err, &exit) {
		t.Fatalf("upgrade should fail in the Go updater before executing Bash, got %v", err)
	}
}

func captureCLI(t *testing.T, input string, call func() error) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "stdin")
	if err := os.WriteFile(inputPath, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()

	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	callErr := call()

	output, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(output), string(diagnostic), callErr
}
