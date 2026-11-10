package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"yagp/internal/nodebridge"
	"yagp/pkg/upgrade"
)

func prepareGost(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("prepare-gost", flag.ExitOnError)
	output := flags.String("output", "", "destination for the Gost executable")
	_ = flags.Parse(args)
	if *output == "" || flags.NArg() != 0 {
		return errors.New("prepare-gost requires --output")
	}
	return downloadGost(ctx, *output, true)
}

func ensureGost(ctx context.Context, configFile string) error {
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}
	if config.NodeID == 0 || !nodebridge.IsLoopbackAddress(config.AdminAddress) {
		return errors.New("node ID and a loopback Gost API address are required")
	}
	basePath, err := filepath.Abs(filepath.Dir(configFile))
	if err != nil {
		return err
	}
	gostConfig := filepath.Join(basePath, "gost.yaml")
	if _, err = os.Stat(gostConfig); err != nil {
		return fmt.Errorf("load Gost configuration: %w", err)
	}
	gostBinary := filepath.Join(basePath, "gost")
	if err = ensureGostBinary(ctx, gostBinary); err != nil {
		return err
	}
	unit := fmt.Sprintf("yagp-gost-%d.service", config.NodeID)
	state, err := exec.CommandContext(ctx, "systemctl", "show", "--property=LoadState", "--value", unit).CombinedOutput()
	switch strings.TrimSpace(string(state)) {
	case "not-found":
		if err = installGostUnit(ctx, unit, basePath, gostBinary, gostConfig); err != nil {
			return err
		}
	case "loaded":
		if err != nil {
			return fmt.Errorf("check Gost service %s: %w", unit, err)
		}
	default:
		return fmt.Errorf("cannot use Gost service %s: %s", unit, strings.TrimSpace(string(state)))
	}
	if exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() != nil {
		if err = systemctl(ctx, "enable", "--now", unit); err != nil {
			return err
		}
	}
	return nodebridge.WaitForGost(ctx, config)
}

func ensureGostBinary(ctx context.Context, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err = downloadGost(ctx, path, false); err != nil {
			return fmt.Errorf("install Gost binary: %w", err)
		}
	} else if err != nil {
		return err
	}
	return nil
}

func installGostUnit(ctx context.Context, unit, basePath, binary, config string) error {
	// This service must be able to start while the bridge's ExecStartPre is running.
	definition := fmt.Sprintf(`[Unit]
Description=yagp managed Gost
After=network.target

[Service]
WorkingDirectory=%s
ExecStart=%q -C %q
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, basePath, binary, config)
	if err := writeNewFile(filepath.Join("/etc/systemd/system", unit), []byte(definition), 0644); err != nil {
		return err
	}
	return systemctl(ctx, "daemon-reload")
}

func systemctl(ctx context.Context, args ...string) error {
	output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func downloadGost(ctx context.Context, path string, showProgress bool) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("Gost installation supports Linux amd64 and arm64")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if showProgress {
		fmt.Fprintln(os.Stderr, "Checking the stable Gost release")
	}
	client := upgrade.NewClient()
	tag, err := gostReleaseTag(ctx, client)
	if err != nil {
		return err
	}
	if showProgress {
		fmt.Fprintf(os.Stderr, "Downloading Gost %s; verifying the archive against GitHub's SHA-256 digest\n", tag)
	}
	attempts := 1
	if showProgress {
		attempts = 3 // Preserve the installer's retries for temporary download failures.
	}
	for attempt := 0; attempt < attempts; attempt++ {
		err = installGostBinary(path, func(dest io.Writer) error {
			return client.Download(ctx, "gost", tag, "", dest)
		})
		if err == nil {
			if showProgress {
				fmt.Fprintln(os.Stderr, "Gost SHA-256 verified; executable installed")
			}
			return nil
		}
		if attempt+1 < attempts {
			fmt.Fprintf(os.Stderr, "Gost download failed: %v; retrying\n", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return err
}

func gostReleaseTag(ctx context.Context, client *upgrade.Client) (string, error) {
	if tag := os.Getenv("YAGP_GOST_VERSION"); tag != "" {
		if !upgrade.ValidateTag("gost", tag) {
			return "", errors.New("YAGP_GOST_VERSION must be a stable Gost tag")
		}
		return tag, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tag, err := client.Latest(ctx, "gost")
	if err != nil {
		return "", fmt.Errorf("look up latest stable Gost release (YAGP_GOST_VERSION can pin a stable tag; GitHub metadata is still required to verify the download): %w", err)
	}
	return tag, nil
}

func installGostBinary(path string, download func(io.Writer) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".gost-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = download(file)
	if err = errors.Join(err, file.Sync(), file.Chmod(0700), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
