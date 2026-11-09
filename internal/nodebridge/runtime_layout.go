package nodebridge

import (
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
const RuntimeLayoutVersion = singleNodeLayoutVersion
const runtimeLayoutFile = ".runtime-layout"
const systemdUnitDir = "/etc/systemd/system"

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

func runtimeUpdateRequired(cfg Config, configFile string) bool {
	if cfg.RuntimeLayout >= RuntimeLayoutVersion || !supportsUpgrade(configFile) {
		return false
	}
	if _, err := os.Stat(filepath.Join(systemdUnitDir, cfg.runtimeUnit("daemon"))); err != nil {
		return false
	}
	return checkSingleNode(filepath.Dir(configFile)) == nil
}

func checkSingleNode(dir string) error {
	root := filepath.Dir(dir)
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
	if cfg.RuntimeLayout >= RuntimeLayoutVersion {
		return nil
	}
	if cfg.NodeID == 0 || !supportsUpgrade(configFile) {
		return errors.New("runtime migration requires an installed Linux systemd node")
	}
	configFile, err := filepath.Abs(configFile)
	if err != nil {
		return err
	}
	dir := filepath.Dir(configFile)
	if err := checkSingleNode(dir); err != nil {
		return err
	}
	return migrateSystemdUnits(ctx, cfg, configFile, systemdUnitDir)
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
	var installedLegacy []string
	for _, unit := range legacy {
		if _, statErr := os.Stat(filepath.Join(unitDir, unit)); statErr == nil {
			installedLegacy = append(installedLegacy, unit)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
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
	if err := WriteNodeFile(marker, []byte(strconv.Itoa(singleNodeLayoutVersion)+"\n")); err != nil {
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
After=network.target nftables.service
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
