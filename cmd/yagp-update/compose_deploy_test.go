package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDeployComposeImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	image := "release.hanzec-overseas.cn/images/yagp@" + digest
	legacyImage := "ghcr.io/hanzec/yagp@" + digest
	currentID := "sha256:" + strings.Repeat("b", 64)
	const projectName = "example"
	const services = "yagp-config-init\nyagp\n"
	const inspectFormat = `{{.Config.Image}}|{{index .Config.Labels "com.docker.compose.project"}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}|{{.Image}}`
	const versionFormat = `{{index .Config.Labels "org.opencontainers.image.version"}}`
	current := func(reference, health string) string {
		return reference + "|" + projectName + "|" + health + "|" + currentID + "\n"
	}
	withCurrent := []string{"config", "list", "inspect", "current-version", "pull", "target-version", "up"}
	firstDeploy := []string{"config", "list", "pull", "target-version", "up"}

	for _, tc := range []struct {
		name      string
		outputs   map[string]string
		exits     map[string]int
		wantSteps []string
		wantError bool
		wantExit  int
	}{
		{name: "deploy newer image", wantSteps: withCurrent},
		{
			name: "skip healthy digest", outputs: map[string]string{"inspect": current(image, "healthy")},
			wantSteps: withCurrent[:3],
		},
		{
			name: "skip healthy legacy registry digest", outputs: map[string]string{"inspect": current(legacyImage, "healthy")},
			wantSteps: withCurrent[:3],
		},
		{
			name:      "run Compose for equal release version",
			outputs:   map[string]string{"current-version": "0.1.149-aaaaaaa\n"},
			wantSteps: withCurrent,
		},
		{
			name:      "retry the same digest when unhealthy",
			outputs:   map[string]string{"inspect": current(image, "unhealthy"), "current-version": "0.1.149-bbbbbbb\n"},
			wantSteps: withCurrent,
		},
		{
			name:      "skip lower numeric version even when it sorts higher as text",
			outputs:   map[string]string{"current-version": "0.1.100-aaaaaaa\n", "target-version": "0.1.99-bbbbbbb\n"},
			wantSteps: withCurrent[:6],
		},
		{
			name: "first deploy without a panel container", outputs: map[string]string{"list": ""},
			wantSteps: firstDeploy,
		},
		{
			name: "reject unrelated service", outputs: map[string]string{"config": services + "unrelated\n"},
			wantSteps: withCurrent[:1], wantError: true,
		},
		{
			name: "stop after config failure", exits: map[string]int{"config": 5},
			wantSteps: withCurrent[:1], wantError: true, wantExit: 5,
		},
		{
			name: "stop if container presence is unknown", exits: map[string]int{"list": 4},
			wantSteps: withCurrent[:2], wantError: true, wantExit: 4,
		},
		{
			name: "reject unexpected container listing", outputs: map[string]string{"list": "yagp\nother\n"},
			wantSteps: withCurrent[:2], wantError: true,
		},
		{
			name: "stop if the existing container cannot be inspected", exits: map[string]int{"inspect": 8},
			wantSteps: withCurrent[:3], wantError: true, wantExit: 8,
		},
		{
			name:      "reject a container from another Compose project",
			outputs:   map[string]string{"inspect": image + "|other|healthy|" + currentID + "\n"},
			wantSteps: withCurrent[:3], wantError: true,
		},
		{
			name:      "stop when the installed image has no release version",
			outputs:   map[string]string{"current-version": "<no value>\n"},
			wantSteps: withCurrent[:4], wantError: true,
		},
		{
			name: "stop when installed image inspection fails", exits: map[string]int{"current-version": 9},
			wantSteps: withCurrent[:4], wantError: true, wantExit: 9,
		},
		{
			name:      "stop when target image has an invalid release version",
			outputs:   map[string]string{"target-version": "dev\n"},
			wantSteps: withCurrent[:6], wantError: true,
		},
		{
			name: "stop when target image inspection fails", exits: map[string]int{"target-version": 11},
			wantSteps: withCurrent[:6], wantError: true, wantExit: 11,
		},
		{
			name: "stop after pull failure", exits: map[string]int{"pull": 7},
			wantSteps: withCurrent[:5], wantError: true, wantExit: 7,
		},
		{
			name: "diagnose failed rebuild", exits: map[string]int{"up": 23, "ps": 17},
			wantSteps: append(slices.Clone(withCurrent), "ps"), wantError: true, wantExit: 23,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			project := composeProject{directory: directory, name: projectName}
			files := map[string]string{
				"docker-compose.yml": "services:\n  yagp:\n    image: existing:latest\n  yagp-config-init:\n    image: existing:latest\n",
				".env":               "YAGP_PUBLIC_HOST=panel.example.test\n",
				".yagp-depoly.yml":   "name: \"example\"\nservices: {}\n",
			}
			for name, content := range files {
				writeComposeTestFile(t, filepath.Join(project.directory, name), content, 0600)
			}

			fixture := t.TempDir()
			docker := filepath.Join(fixture, "docker")
			writeComposeTestFile(t, docker, composeDockerFixture, 0700)
			outputs := map[string]string{
				"config": services, "list": "yagp\n", "inspect": current("release.hanzec-overseas.cn/images/yagp:latest", "healthy"),
				"current-version": "0.1.148-aaaaaaa\n", "target-version": "0.1.149-bbbbbbb\n",
			}
			for step, value := range tc.outputs {
				outputs[step] = value
			}
			for step, value := range outputs {
				writeComposeTestFile(t, filepath.Join(fixture, step+"-output"), value, 0600)
			}
			for _, step := range []string{"config", "list", "inspect", "current-version", "target-version", "pull", "up", "ps"} {
				writeComposeTestFile(t, filepath.Join(fixture, step+"-exit"), strconv.Itoa(tc.exits[step]), 0600)
			}
			t.Setenv("DOCKER_HOST", "unix:///unused-test-docker.sock")

			var output bytes.Buffer
			err = deployComposeImage(t.Context(), project, docker, digest, &output)
			if (err != nil) != tc.wantError {
				t.Fatalf("deployment returned %v, want error %v; output %q", err, tc.wantError, output.String())
			}
			if tc.wantExit != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit {
					t.Fatalf("deployment error = %v, want Docker exit %d", err, tc.wantExit)
				}
			}

			base := []string{"compose", "--project-name", project.name, "--project-directory", project.directory,
				"--env-file", filepath.Join(project.directory, ".env"), "-f", filepath.Join(project.directory, "docker-compose.yml")}
			commands := map[string][]string{
				"config":          append(slices.Clone(base), "config", "--services"),
				"list":            {"container", "ls", "--all", "--filter", "name=^/yagp$", "--format", "{{.Names}}"},
				"inspect":         {"inspect", "--type", "container", "--format", inspectFormat, "yagp"},
				"current-version": {"image", "inspect", "--format", versionFormat, currentID},
				"pull":            {"image", "pull", image},
				"target-version":  {"image", "inspect", "--format", versionFormat, image},
				"up":              append(slices.Clone(base), "-f", "-", "up", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120", "yagp", "yagp-config-init"),
				"ps":              append(slices.Clone(base), "-f", "-", "ps", "--all", "yagp", "yagp-config-init"),
			}
			log, err := os.ReadFile(filepath.Join(fixture, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSuffix(string(log), "\n"), "\n")
			if len(calls) != len(tc.wantSteps) {
				t.Fatalf("Docker calls = %q, want steps %v", calls, tc.wantSteps)
			}
			for i, step := range tc.wantSteps {
				got := strings.Split(strings.TrimSuffix(calls[i], "\x00"), "\x00")
				want := append([]string{"--host", "unix:///var/run/docker.sock"}, commands[step]...)
				if !slices.Equal(got, want) {
					t.Fatalf("Docker %s args = %q, want %q", step, got, want)
				}
				if step == "up" || step == "ps" {
					checkComposeOverride(t, filepath.Join(fixture, step+"-stdin"), image)
				}
				if step == "pull" || step == "up" || step == "ps" {
					for _, stream := range []string{"stdout", "stderr"} {
						if !strings.Contains(output.String(), step+" "+stream) {
							t.Fatalf("Docker %s %s not forwarded: %q", step, stream, output.String())
						}
					}
				}
			}

			entries, err := os.ReadDir(project.directory)
			if err != nil || len(entries) != len(files) {
				t.Fatalf("project files changed: %v (%v)", entries, err)
			}
			for name, want := range files {
				data, err := os.ReadFile(filepath.Join(project.directory, name))
				if err != nil || string(data) != want {
					t.Fatalf("project file %s changed: %q (%v)", name, data, err)
				}
			}
		})
	}
}

func writeComposeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func checkComposeOverride(t *testing.T, path, image string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]map[string]string
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]map[string]string{
		"services": {
			"yagp":             {"image": image},
			"yagp-config-init": {"image": image},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stdin Compose override = %s; want both image fields only", data)
	}
}

const composeDockerFixture = `#!/bin/sh
set -eu
fixture=${0%/*}
[ -z "${DOCKER_HOST:-}" ]
[ "$HOME" = /root ]
[ "$COMPOSE_REMOVE_ORPHANS" = false ]
[ "$PWD" = "$(pwd -P)" ]
{
    printf '%s\0' "$@"
    printf '\n'
} >> "$fixture/calls"
shift 2
case "$1" in
    container) step=list ;;
    inspect) step=inspect ;;
    image)
        case "$2:${5:-}" in
            inspect:sha256:*) step=current-version ;;
            inspect:*) step=target-version ;;
            pull:*) step=pull ;;
        esac ;;
    compose)
        shift
        while [ "$#" -gt 0 ]; do
            case "$1" in
                config) step=config; break ;;
                up|ps) step=$1; cat > "$fixture/$step-stdin"; break ;;
            esac
            shift
        done ;;
esac
case "$step" in
    pull|up|ps)
        printf '%s stdout\n' "$step"
        printf '%s stderr\n' "$step" >&2 ;;
    *) cat "$fixture/$step-output" ;;
esac
exit "$(cat "$fixture/$step-exit")"
`
