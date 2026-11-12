package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var (
	composeProjectPath = regexp.MustCompile(`^(/[a-zA-Z0-9_.-]+)+$`)
	composeProjectName = regexp.MustCompile(`^name: "([a-z0-9][a-z0-9_-]*)"\n$`)
)

type composeProject struct {
	directory string
	name      string
}

func loadComposeProject(stateDir string) (composeProject, error) {
	marker := filepath.Join(stateDir, "project-dir")
	// Older installers store the Compose project name in the managed override.
	override := filepath.Join(stateDir, "compose.yml")
	for _, file := range []string{marker, override} {
		if err := checkComposeProjectFile(file); err != nil {
			return composeProject{}, err
		}
	}

	storedPath, err := os.ReadFile(marker)
	if err != nil {
		return composeProject{}, err
	}
	directory, err := resolveComposeProjectDirectory(strings.TrimRight(string(storedPath), "\n"))
	if err != nil {
		return composeProject{}, err
	}
	name, err := readComposeProjectName(override)
	if err != nil {
		return composeProject{}, err
	}
	return composeProject{directory: directory, name: name}, nil
}

func resolveComposeProjectDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("Compose project directory must be absolute: %q", path)
	}
	directory, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Compose project directory: %w", err)
	}
	if !composeProjectPath.MatchString(directory) {
		return "", fmt.Errorf("invalid Compose project directory: %q", directory)
	}
	if err := checkSelfUpgradePath(directory, false); err != nil {
		return "", err
	}
	for _, file := range []string{"docker-compose.yml", ".env"} {
		if err := checkComposeProjectFile(filepath.Join(directory, file)); err != nil {
			return "", err
		}
	}
	for _, bind := range []string{"config", "data", "logs"} {
		if err := checkSelfUpgradePath(filepath.Join(directory, bind), false); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func readComposeProjectName(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	firstLine, err := bufio.NewReader(file).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read Compose project name from %s: %w", path, err)
	}
	match := composeProjectName.FindStringSubmatch(firstLine)
	if match == nil {
		return "", fmt.Errorf("invalid Compose project name in %s", path)
	}
	return match[1], nil
}

func checkComposeProjectFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("Compose project file is not root-controlled: %s", path)
	}
	return nil
}
