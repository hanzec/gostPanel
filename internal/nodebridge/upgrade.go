package nodebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"yagp/internal/buildinfo"
	"yagp/pkg/upgrade"
)

const upgradeStatusFile = ".upgrade-status.json"

type UpgradeStatus struct {
	Supported             bool   `json:"-"`
	RuntimeUpdateRequired bool   `json:"-"`
	DaemonVersion         string `json:"-"`
	GostVersion           string `json:"-"`
	Component             string `json:"component"`
	Tag                   string `json:"tag"`
	State                 string `json:"state"`
	Message               string `json:"message"`
}

// StartUpgrade launches a separate service so restarting yagp-daemon cannot stop the worker.
func StartUpgrade(ctx context.Context, cfg Config, configPath, component, tag string) error {
	if !upgrade.ValidateTag(component, tag) {
		return errors.New("invalid upgrade component or release tag")
	}
	if !supportsUpgrade(configPath) {
		return errors.New("remote upgrades require a systemd-managed Linux node")
	}
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(configPath)
	return upgradeCommand(ctx, "systemd-run", "--unit="+upgradeUnit(cfg.NodeID), "--collect",
		"--property=Type=exec", "--working-directory="+dir, "--", filepath.Join(dir, "yagp-daemon"),
		"upgrade-component", "-c", configPath, "--component", component, "--tag", tag)
}

// ReadUpgradeStatus returns installed versions and the latest upgrade operation.
func ReadUpgradeStatus(ctx context.Context, cfg Config, configPath string) (UpgradeStatus, error) {
	status := UpgradeStatus{}
	supported := supportsUpgrade(configPath)
	if configPath != "" {
		dir := filepath.Dir(configPath)
		var err error
		if status, err = readUpgradeOperation(dir); err != nil {
			return UpgradeStatus{}, err
		}
		if supported && status.State == "running" && upgradeWorkerStopped(ctx, cfg.NodeID) {
			// The worker writes the result before exiting, so check once more after it stops.
			if status, err = readUpgradeOperation(dir); err != nil {
				return UpgradeStatus{}, err
			}
			if status.State == "running" {
				status.State, status.Message = "failed", "upgrade worker stopped before reporting its result"
			}
		}
		status.GostVersion, _ = executableVersion(ctx, filepath.Join(dir, "gost"), "gost")
	}
	status.Supported = supported
	status.RuntimeUpdateRequired = runtimeUpdateRequired(cfg, configPath)
	status.DaemonVersion = buildinfo.Version
	return status, nil
}

func readUpgradeOperation(dir string) (UpgradeStatus, error) {
	var status UpgradeStatus
	data, err := os.ReadFile(filepath.Join(dir, upgradeStatusFile))
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	err = json.Unmarshal(data, &status)
	return status, err
}

func upgradeWorkerStopped(ctx context.Context, nodeID uint64) bool {
	// systemctl prints the state even when a transient unit is inactive or collected.
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", upgradeUnit(nodeID)).Output()
	switch strings.TrimSpace(string(out)) {
	case "inactive", "failed", "unknown":
		return true
	default:
		return false
	}
}

func supportsUpgrade(configPath string) bool {
	if configPath == "" || runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return false
	}
	if info, err := os.Stat("/run/systemd/system"); err != nil || !info.IsDir() {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

func upgradeUnit(id uint64) string {
	return fmt.Sprintf("yagp-upgrade-%d.service", id)
}

func executableVersion(ctx context.Context, path, component string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	flag := "--version"
	if component == "gost" {
		flag = "-V"
	}
	out, err := exec.CommandContext(ctx, path, flag).Output()
	if err != nil {
		return "", err
	}
	if component == "daemon" {
		return strings.TrimSpace(string(out)), nil
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "gost" {
		return "", errors.New("unexpected Gost version output")
	}
	return "v" + strings.TrimPrefix(fields[1], "v"), nil
}

func upgradeCommand(ctx context.Context, command string, args ...string) error {
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeUpgradeStatus(dir string, status UpgradeStatus) error {
	file, err := os.CreateTemp(dir, ".upgrade-status-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(status)
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, upgradeStatusFile))
}
