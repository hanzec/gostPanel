package nodebridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yagp/pkg/nodegrpc"
)

const singleNodeLayoutVersion = 1
const networkOrderLayoutVersion = 2
const RuntimeLayoutVersion = 3
const DefaultNodeStateDir = "/etc/yagp-daemon"
const runtimeLayoutFile = ".runtime-layout"
const systemdUnitDir = "/etc/systemd/system"
const redirectOrderDropIn = "50-yagp-network.conf"

func ReadRuntimeLayout(configFile string) int {
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(configFile), runtimeLayoutFile))
	version, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return version
}

func (cfg Config) runtimeUnit(component string) string {
	if cfg.RuntimeLayout >= singleNodeLayoutVersion {
		return "yagp-" + component + ".service"
	}
	return fmt.Sprintf("yagp-%s-%d.service", component, cfg.NodeID)
}

func runtimeUpdateRequired(cfg Config, configFile, unitDir string) bool {
	if !supportsUpgrade(configFile) {
		return false
	}
	configFile, err := filepath.Abs(configFile)
	if err != nil || max(cfg.RuntimeLayout, ReadRuntimeLayout(configFile)) >= runtimeLayoutTarget(cfg, configFile) {
		return false
	}
	if _, err := os.Stat(filepath.Join(unitDir, cfg.runtimeUnit("daemon"))); err != nil {
		return false
	}
	return checkSingleNode(filepath.Dir(configFile)) == nil
}

func runtimeLayoutTarget(cfg Config, configFile string) int {
	flat := filepath.Join(DefaultNodeStateDir, "node.yaml")
	if configFile == flat || runtimeConfigPath(cfg, configFile) == flat {
		return RuntimeLayoutVersion
	}
	return networkOrderLayoutVersion
}

func runtimeConfigPath(cfg Config, configFile string) string {
	if configFile == filepath.Join(DefaultNodeStateDir, strconv.FormatUint(cfg.NodeID, 10), "node.yaml") {
		return filepath.Join(DefaultNodeStateDir, "node.yaml")
	}
	return configFile
}

func checkSingleNode(dir string) error {
	root := filepath.Dir(dir)
	if dir == DefaultNodeStateDir {
		root = dir
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.ParseUint(entry.Name(), 10, 64); err != nil || !entry.IsDir() {
			continue
		}
		other := filepath.Join(root, entry.Name())
		if other == dir {
			continue
		}
		if _, err := os.Stat(filepath.Join(other, "yagp-daemon")); err == nil {
			return fmt.Errorf("another node is installed in %s; only one node per server is supported", other)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// UpdateSystemdRuntime migrates the versioned unit layout from an external worker.
func UpdateSystemdRuntime(ctx context.Context, cfg Config, configFile string) error {
	configFile, err := filepath.Abs(configFile)
	if err != nil {
		return err
	}
	version := runtimeLayoutTarget(cfg, configFile)
	if cfg.RuntimeLayout >= version {
		return nil
	}
	if cfg.NodeID == 0 || !supportsUpgrade(configFile) {
		return errors.New("runtime migration requires an installed Linux systemd node")
	}
	dir := filepath.Dir(configFile)
	if err := checkSingleNode(dir); err != nil {
		return err
	}
	if destination := runtimeConfigPath(cfg, configFile); destination != configFile {
		return migrateFlatSystemdRuntime(ctx, cfg, configFile, destination, systemdUnitDir)
	}
	if cfg.RuntimeLayout >= singleNodeLayoutVersion {
		return updateSystemdNetworkOrder(ctx, cfg, configFile, systemdUnitDir)
	}
	return migrateSystemdUnits(ctx, cfg, configFile, systemdUnitDir)
}

func updateSystemdNetworkOrder(ctx context.Context, cfg Config, configFile, unitDir string) error {
	dir := filepath.Join(unitDir, cfg.runtimeUnit("daemon")+".d")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, redirectOrderDropIn), []byte("[Unit]\nAfter=nftables.service docker.service\n"), 0644); err != nil {
		return err
	}
	if err := upgradeCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return saveRuntimeLayout(configFile, runtimeLayoutTarget(cfg, configFile))
}

func saveRuntimeLayout(configFile string, version int) error {
	return WriteNodeFile(filepath.Join(filepath.Dir(configFile), runtimeLayoutFile), []byte(strconv.Itoa(version)+"\n"))
}

func migrateFlatSystemdRuntime(ctx context.Context, cfg Config, oldConfig, newConfig, unitDir string) (err error) {
	oldDir, newDir := filepath.Dir(oldConfig), filepath.Dir(newConfig)
	units := []string{"yagp-daemon.service", "yagp-gost.service"}
	previousUnits, err := installedRuntimeUnits(unitDir, cfg.runtimeUnit("daemon"), cfg.runtimeUnit("gost"))
	if err != nil {
		return err
	}
	definitions := runtimeUnitDefinitions(newDir, newConfig)
	var undo []func() error
	stopped := false
	started := false
	defer func() {
		if err == nil || !stopped {
			return
		}
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if started {
			err = errors.Join(err, upgradeCommand(recovery, "systemctl", "disable", "--now", units[1], units[0]))
			if cfg.RuntimeLayout < singleNodeLayoutVersion {
				removeSingleNodeTables(recovery)
			}
		}
		for i := len(undo) - 1; i >= 0; i-- {
			err = errors.Join(err, undo[i]())
		}
		err = errors.Join(err, upgradeCommand(recovery, "systemctl", "daemon-reload"))
		if len(previousUnits) != 0 {
			err = errors.Join(err, upgradeCommand(recovery, "systemctl", append([]string{"enable", "--now"}, previousUnits...)...))
		}
	}()

	for i, unit := range units {
		path := filepath.Join(unitDir, unit)
		info, statErr := os.Lstat(path)
		if cfg.RuntimeLayout < singleNodeLayoutVersion {
			if !errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("cannot replace existing unit %s", unit)
			}
			continue
		}
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot migrate non-regular unit %s", unit)
		}
		original, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// Update the stock unit's paths while retaining administrator modifications.
		definitions[i] = strings.NewReplacer(oldDir+"/", newDir+"/", "WorkingDirectory="+oldDir+"\n", "WorkingDirectory="+newDir+"\n").Replace(string(original))
		undo = append(undo, func() error { return os.WriteFile(path, original, info.Mode().Perm()) })
	}
	if err := checkFlatMigrationFiles(oldDir, newDir); err != nil {
		return err
	}
	stopped = true
	if len(previousUnits) != 0 {
		stop := []string{"stop"}
		if cfg.RuntimeLayout < singleNodeLayoutVersion {
			stop = []string{"disable", "--now"}
		}
		if err := upgradeCommand(ctx, "systemctl", append(stop, previousUnits...)...); err != nil {
			return err
		}
	}
	previousOperation, _ := readUpgradeOperation(oldDir)
	if err := moveFlatMigrationFiles(oldDir, newDir, &undo); err != nil {
		return err
	}
	for i, unit := range units {
		path := filepath.Join(unitDir, unit)
		if cfg.RuntimeLayout >= singleNodeLayoutVersion {
			if err := os.WriteFile(path, []byte(definitions[i]), 0644); err != nil {
				return err
			}
			continue
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		undo = append(undo, func() error { return os.Remove(path) })
		_, err = file.WriteString(definitions[i])
		if err := errors.Join(err, file.Close()); err != nil {
			return err
		}
	}
	if cfg.RuntimeLayout == singleNodeLayoutVersion {
		if err := ensureNetworkOrderDropIn(filepath.Join(unitDir, units[0]+".d"), &undo); err != nil {
			return err
		}
	}
	if err := upgradeCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	started = true
	if err := upgradeCommand(ctx, "systemctl", "enable", "--now", units[0], units[1]); err != nil {
		return err
	}
	if err := waitForDaemon(ctx, cfg, ""); err != nil {
		return err
	}
	if err := WaitForGost(ctx, cfg); err != nil {
		return err
	}
	if cfg.RuntimeLayout < singleNodeLayoutVersion {
		for _, unit := range previousUnits {
			_ = os.Remove(filepath.Join(unitDir, unit))
		}
		_ = upgradeCommand(ctx, "systemctl", "daemon-reload")
	}
	// A worker from the previous layout can still need to write its final status here.
	if previousOperation.State != "running" {
		_ = os.Remove(oldDir)
	}
	return nil
}

func installedRuntimeUnits(unitDir string, units ...string) ([]string, error) {
	var installed []string
	for _, unit := range units {
		if _, err := os.Stat(filepath.Join(unitDir, unit)); err == nil {
			installed = append(installed, unit)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return installed, nil
}

func ensureNetworkOrderDropIn(dir string, undo *[]func() error) error {
	if err := os.Mkdir(dir, 0755); err == nil {
		*undo = append(*undo, func() error { return os.Remove(dir) })
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	path := filepath.Join(dir, redirectOrderDropIn)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	*undo = append(*undo, func() error { return os.Remove(path) })
	_, err = file.WriteString("[Unit]\nAfter=nftables.service docker.service\n")
	return errors.Join(err, file.Close())
}

func checkFlatMigrationFiles(oldDir, newDir string) error {
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		destination := filepath.Join(newDir, entry.Name())
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot move node state over %s", destination)
		}
	}
	if _, err := os.Lstat(filepath.Join(newDir, runtimeLayoutFile)); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot replace existing runtime layout in %s", newDir)
	}
	return nil
}

func moveFlatMigrationFiles(oldDir, newDir string, undo *[]func() error) error {
	if err := checkFlatMigrationFiles(oldDir, newDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		source := filepath.Join(oldDir, entry.Name())
		dest := filepath.Join(newDir, entry.Name())
		if err := os.Rename(source, dest); err != nil {
			return err
		}
		*undo = append(*undo, func() error { return os.Rename(dest, source) })
	}
	for _, name := range []string{"node.yaml", "gost.yaml", runtimeLayoutFile} {
		path := filepath.Join(newDir, name)
		info, err := os.Lstat(path)
		if name == runtimeLayoutFile && errors.Is(err, os.ErrNotExist) {
			*undo = append(*undo, func() error {
				err := os.Remove(path)
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			})
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot migrate non-regular node configuration %s", path)
		}
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		*undo = append(*undo, func() error { return os.WriteFile(path, original, info.Mode().Perm()) })
		if name != runtimeLayoutFile {
			updated := bytes.ReplaceAll(original, []byte(oldDir+"/"), []byte(newDir+"/"))
			if !bytes.Equal(original, updated) {
				if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
					return err
				}
			}
		}
	}
	return saveRuntimeLayout(filepath.Join(newDir, "node.yaml"), RuntimeLayoutVersion)
}

func migrateSystemdUnits(ctx context.Context, cfg Config, configFile, unitDir string) (err error) {
	dir := filepath.Dir(configFile)
	legacy := []string{cfg.runtimeUnit("daemon"), cfg.runtimeUnit("gost")}
	fixed := []string{"yagp-daemon.service", "yagp-gost.service"}
	for _, unit := range fixed {
		if _, err := os.Lstat(filepath.Join(unitDir, unit)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot replace existing unit %s", unit)
		}
	}
	installedLegacy, err := installedRuntimeUnits(unitDir, legacy...)
	if err != nil {
		return err
	}
	marker := filepath.Join(dir, runtimeLayoutFile)
	var created []string
	started := false
	defer func() {
		if err == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if started {
			_ = upgradeCommand(recovery, "systemctl", "disable", "--now", fixed[1], fixed[0])
			removeSingleNodeTables(recovery)
		}
		for _, file := range created {
			_ = os.Remove(file)
		}
		_ = os.Remove(marker)
		_ = upgradeCommand(recovery, "systemctl", "daemon-reload")
		if len(installedLegacy) != 0 {
			err = errors.Join(err, upgradeCommand(recovery, "systemctl", append([]string{"enable", "--now"}, installedLegacy...)...))
		}
	}()
	for i, definition := range runtimeUnitDefinitions(dir, configFile) {
		path := filepath.Join(unitDir, fixed[i])
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		created = append(created, path)
		_, err = file.WriteString(definition)
		if err = errors.Join(err, file.Close()); err != nil {
			return err
		}
	}
	if len(installedLegacy) != 0 {
		if err := upgradeCommand(ctx, "systemctl", append([]string{"disable", "--now"}, installedLegacy...)...); err != nil {
			return err
		}
	}
	if err := saveRuntimeLayout(configFile, runtimeLayoutTarget(cfg, configFile)); err != nil {
		return err
	}
	if err := upgradeCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	started = true
	if err := upgradeCommand(ctx, "systemctl", "enable", "--now", fixed[0], fixed[1]); err != nil {
		return err
	}
	if err := waitForDaemon(ctx, cfg, ""); err != nil {
		return err
	}
	if err := WaitForGost(ctx, cfg); err != nil {
		return err
	}
	for _, unit := range installedLegacy {
		_ = os.Remove(filepath.Join(unitDir, unit))
	}
	_ = upgradeCommand(ctx, "systemctl", "daemon-reload")
	return nil
}

func runtimeUnitDefinitions(dir, configFile string) [2]string {
	daemon := filepath.Join(dir, "yagp-daemon")
	return [2]string{
		fmt.Sprintf(`[Unit]
Description=yagp node daemon
After=network.target nftables.service docker.service
Wants=yagp-gost.service

[Service]
Type=exec
WorkingDirectory=%s
ExecStart=%q -c %q
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, dir, daemon, configFile),
		fmt.Sprintf(`[Unit]
Description=yagp managed Gost
BindsTo=yagp-daemon.service
After=yagp-daemon.service
PartOf=yagp-daemon.service

[Service]
Type=exec
WorkingDirectory=%s
ExecStartPre=%q ensure-gost-binary -c %q
ExecStart=%q -C %q
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, dir, daemon, configFile, filepath.Join(dir, "gost"), filepath.Join(dir, "gost.yaml")),
	}
}

func removeSingleNodeTables(ctx context.Context) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return
	}
	batch := fmt.Appendf(nil, "add table inet %[1]s\ndelete table inet %[1]s\n", nodegrpc.SingleNodeNftTable)
	batch = append(batch, trafficQuotaBatch(singleNodeQuotaTable, trafficQuotaState{})...)
	_ = runNft(ctx, nft, batch, "-f", "-")
}
