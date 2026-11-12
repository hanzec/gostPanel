package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadComposeProjectName(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"installer output", "name: \"edge_2-panel\"\nservices: {}\n", "edge_2-panel"},
		{"invalid project", "name: \"Edge Panel\"\n", ""},
		{"unquoted project", "name: edge-panel\n", ""},
		{"not first line", "# override\nname: \"edge-panel\"\n", ""},
		{"incomplete line", "name: \"edge-panel\"", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			name, err := readComposeProjectName(path)
			if (err == nil) != (tc.want != "") || name != tc.want {
				t.Fatalf("project name = %q, error = %v, want %q", name, err, tc.want)
			}
		})
	}
}

func TestLoadComposeProjectReadsRootControlledInstallation(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("requires a root-controlled Linux directory")
	}
	root, err := os.MkdirTemp("/var/lib", "yagp-compose-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	stateDir := filepath.Join(root, "state")
	projectDir := filepath.Join(root, "panel")
	for _, dir := range []string{stateDir, projectDir, filepath.Join(projectDir, "config"), filepath.Join(projectDir, "data"), filepath.Join(projectDir, "logs")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	projectLink := filepath.Join(root, "panel-link")
	if err := os.Symlink(projectDir, projectLink); err != nil {
		t.Fatal(err)
	}
	const override = "name: \"edge-panel\"\nservices: {}\n"
	for path, contents := range map[string]string{
		filepath.Join(stateDir, "project-dir"):          projectLink + "\n",
		filepath.Join(stateDir, "compose.yml"):          override,
		filepath.Join(projectDir, "docker-compose.yml"): "services: {}\n",
		filepath.Join(projectDir, ".env"):               "",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := loadComposeProject(stateDir)
	if err != nil || project != (composeProject{directory: canonical, name: "edge-panel"}) {
		t.Fatalf("project = %+v, error = %v", project, err)
	}
	if got, err := os.ReadFile(filepath.Join(stateDir, "compose.yml")); err != nil || string(got) != override {
		t.Fatalf("override changed while loading project: %q, %v", got, err)
	}

	t.Run("rejects a writable state file", func(t *testing.T) {
		marker := filepath.Join(stateDir, "project-dir")
		if err := os.Chmod(marker, 0o620); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })
		if _, err := loadComposeProject(stateDir); err == nil || !strings.Contains(err.Error(), marker) {
			t.Fatalf("writable marker returned %v", err)
		}
	})
	t.Run("rejects a symlink for a Compose file", func(t *testing.T) {
		env := filepath.Join(projectDir, ".env")
		if err := os.Rename(env, env+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(env+".original", env); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Rename(env+".original", env) })
		if _, err := loadComposeProject(stateDir); err == nil || !strings.Contains(err.Error(), ".env") {
			t.Fatalf("symlinked .env returned %v", err)
		}
	})
	t.Run("rejects a writable bind directory", func(t *testing.T) {
		logs := filepath.Join(projectDir, "logs")
		if err := os.Chmod(logs, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := loadComposeProject(stateDir); err == nil || !strings.Contains(err.Error(), "/logs") {
			t.Fatalf("writable bind directory returned %v", err)
		}
	})
}
