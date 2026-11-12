package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"yagp/pkg/upgrade"
)

const (
	panelImageRepository       = "release.hanzec-overseas.cn/images/yagp"
	legacyPanelImageRepository = "ghcr.io/hanzec/yagp"
	panelInspectFormat         = `{{.Config.Image}}|{{index .Config.Labels "com.docker.compose.project"}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}|{{.Image}}`
	panelVersionFormat         = `{{index .Config.Labels "org.opencontainers.image.version"}}`
)

type currentPanelImage struct {
	reference string
	id        string
	healthy   bool
}

func runNativeComposeUpdate(ctx context.Context, digest string, output io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("panel deployment requires Linux")
	}
	stateDir := filepath.Dir(selfUpgradeLockPath)
	if err := checkSelfUpgradePath(stateDir, false); err != nil {
		return err
	}
	lock, err := acquireSelfUpgradeLock(ctx, selfUpgradeLockPath)
	if err != nil {
		return err
	}
	defer lock.Close()

	project, err := loadComposeProject(stateDir)
	if err != nil {
		return err
	}
	docker, err := findRootOwnedTool("docker", strings.Split(strings.TrimPrefix(updatePath, "PATH="), ":"))
	if err != nil {
		return err
	}
	return deployComposeImage(ctx, project, docker, digest, output)
}

func deployComposeImage(ctx context.Context, project composeProject, docker, digest string, output io.Writer) error {
	config := composeCommand(ctx, project, docker, "config", "--services")
	config.Stderr = output
	data, err := config.Output()
	if err != nil {
		return fmt.Errorf("read Compose services: %w", err)
	}
	services := strings.Fields(string(data))
	slices.Sort(services)
	if !slices.Equal(services, []string{"yagp", "yagp-config-init"}) {
		return errors.New("Compose project must contain only yagp and yagp-config-init")
	}

	image := panelImageRepository + "@" + digest
	current, err := inspectPanelContainer(ctx, project, docker, output)
	if err != nil {
		return err
	}
	if current.healthy && (current.reference == image || current.reference == legacyPanelImageRepository+"@"+digest) {
		log.Printf("project %s already runs healthy image %s", project.name, digest)
		return nil
	}
	var currentVersion string
	if current.id != "" {
		currentVersion, err = readPanelImageVersion(ctx, project, docker, current.id, output)
		if err != nil {
			return fmt.Errorf("read current panel version: %w", err)
		}
	}

	log.Printf("pulling panel image %s", image)
	pull := dockerCommand(ctx, project, docker, "image", "pull", image)
	pull.Stdout, pull.Stderr = output, output
	if err := pull.Run(); err != nil {
		return fmt.Errorf("pull panel image: %w", err)
	}
	targetVersion, err := readPanelImageVersion(ctx, project, docker, image, output)
	if err != nil {
		return fmt.Errorf("read target panel version: %w", err)
	}
	if currentVersion != "" && upgrade.IsNewer("daemon", targetVersion, currentVersion) {
		log.Printf("skipping panel downgrade from %s to %s", currentVersion, targetVersion)
		return nil
	}

	// Pass only the image selection to Compose. The on-disk project stays read-only.
	override := fmt.Sprintf("services:\n  yagp:\n    image: %s\n  yagp-config-init:\n    image: %s\n", image, image)
	log.Printf("recreating panel services in %s with version %s and waiting for health", project.name, targetVersion)
	if err := runImageComposeCommand(ctx, project, docker, override, output,
		"up", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120", "yagp", "yagp-config-init"); err != nil {
		_ = runImageComposeCommand(ctx, project, docker, override, output, "ps", "--all", "yagp", "yagp-config-init")
		return fmt.Errorf("wait for updated Compose services: %w", err)
	}
	log.Printf("panel image %s is healthy in project %s", digest, project.name)
	return nil
}

func inspectPanelContainer(ctx context.Context, project composeProject, docker string, output io.Writer) (currentPanelImage, error) {
	list := dockerCommand(ctx, project, docker, "container", "ls", "--all", "--filter", "name=^/yagp$", "--format", "{{.Names}}")
	list.Stderr = output
	names, err := list.Output()
	if err != nil {
		return currentPanelImage{}, fmt.Errorf("list panel container: %w", err)
	}
	name := strings.TrimSpace(string(names))
	if name == "" {
		return currentPanelImage{}, nil
	}
	if name != "yagp" {
		return currentPanelImage{}, errors.New("could not identify the yagp container")
	}
	inspect := dockerCommand(ctx, project, docker, "inspect", "--type", "container", "--format", panelInspectFormat, name)
	inspect.Stderr = output
	data, err := inspect.Output()
	if err != nil {
		return currentPanelImage{}, fmt.Errorf("inspect current panel container: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(data)), "|")
	if len(fields) != 4 || fields[1] != project.name || fields[3] == "" {
		return currentPanelImage{}, fmt.Errorf("could not identify the current panel image for Compose project %s", project.name)
	}
	return currentPanelImage{reference: fields[0], healthy: fields[2] == "healthy", id: fields[3]}, nil
}

func readPanelImageVersion(ctx context.Context, project composeProject, docker, image string, output io.Writer) (string, error) {
	cmd := dockerCommand(ctx, project, docker, "image", "inspect", "--format", panelVersionFormat, image)
	cmd.Stderr = output
	data, err := cmd.Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if !upgrade.ValidateTag("daemon", version) {
		return "", fmt.Errorf("image %s lacks a valid YAGP release version label", image)
	}
	return version, nil
}

func runImageComposeCommand(ctx context.Context, project composeProject, docker, override string, output io.Writer, args ...string) error {
	cmd := composeCommand(ctx, project, docker, append([]string{"-f", "-"}, args...)...)
	cmd.Stdin = strings.NewReader(override)
	cmd.Stdout, cmd.Stderr = output, output
	return cmd.Run()
}

func composeCommand(ctx context.Context, project composeProject, docker string, args ...string) *exec.Cmd {
	dir := project.directory
	base := []string{"compose", "--project-name", project.name, "--project-directory", dir,
		"--env-file", filepath.Join(dir, ".env"), "-f", filepath.Join(dir, "docker-compose.yml")}
	return dockerCommand(ctx, project, docker, append(base, args...)...)
}

func dockerCommand(ctx context.Context, project composeProject, docker string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, docker, append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	cmd.Dir = project.directory
	cmd.Env = []string{updatePath, "HOME=/root", "PWD=" + project.directory, "COMPOSE_REMOVE_ORPHANS=false"}
	return cmd
}
