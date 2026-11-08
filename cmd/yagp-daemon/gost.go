package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"yagp/internal/nodebridge"
)

const gostVersion = "3.2.6"

func prepareGost(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("prepare-gost", flag.ExitOnError)
	output := flags.String("output", "", "destination for the Gost executable")
	_ = flags.Parse(args)
	if *output == "" || flags.NArg() != 0 {
		return errors.New("prepare-gost requires --output")
	}
	return downloadGost(ctx, *output)
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
	if _, err = os.Stat(gostBinary); errors.Is(err, os.ErrNotExist) {
		if err = downloadGost(ctx, gostBinary); err != nil {
			return fmt.Errorf("install Gost binary: %w", err)
		}
	} else if err != nil {
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

func downloadGost(ctx context.Context, path string) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("Gost installation supports Linux amd64 and arm64")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	url := fmt.Sprintf("%shttps://github.com/go-gost/gost/releases/download/v%s/gost_%s_linux_%s.tar.gz",
		os.Getenv("GH_PROXY"), gostVersion, gostVersion, runtime.GOARCH)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download Gost: %s", res.Status)
	}
	compressed, err := gzip.NewReader(res.Body)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("Gost archive has no executable")
		}
		if err != nil {
			return err
		}
		if filepath.Clean(header.Name) == "gost" && header.Typeflag == tar.TypeReg && header.Size > 0 {
			return installGostBinary(path, archive)
		}
	}
}

func installGostBinary(path string, source io.Reader) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".gost-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = io.Copy(file, source)
	if err = errors.Join(err, file.Chmod(0700), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
