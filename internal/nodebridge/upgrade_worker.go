package nodebridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"yagp/internal/buildinfo"
	"yagp/pkg/nodegrpc"
	"yagp/pkg/upgrade"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// RunUpgrade performs an upgrade in the independent worker service.
func RunUpgrade(ctx context.Context, cfg Config, configPath, component, tag string) error {
	return runUpgrade(ctx, cfg, configPath, component, tag, func(dest io.Writer) error {
		downloadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return upgrade.NewClient().Download(downloadCtx, component, tag, "systemd", dest)
	})
}

// UpgradeInstalledDaemon uses the installer-provided executable and the same transaction as remote upgrades.
func UpgradeInstalledDaemon(ctx context.Context, cfg Config, configPath string, binary io.Reader) error {
	return runUpgrade(ctx, cfg, configPath, "daemon", buildinfo.Version, func(dest io.Writer) error {
		_, err := io.Copy(dest, binary)
		return err
	})
}

func runUpgrade(ctx context.Context, cfg Config, configPath, component, tag string, writeBinary func(io.Writer) error) error {
	if !upgrade.ValidateTag(component, tag) || cfg.NodeID == 0 {
		return errors.New("invalid node, upgrade component or release tag")
	}
	if !supportsUpgrade(configPath) {
		return errors.New("upgrades require a systemd-managed Linux node")
	}
	dir := filepath.Dir(configPath)
	status := UpgradeStatus{Component: component, Tag: tag, State: "running"}
	if err := writeUpgradeStatus(dir, status); err != nil {
		return fmt.Errorf("record upgrade start: %w", err)
	}
	err := installUpgrade(ctx, cfg, dir, component, tag, writeBinary)
	status.State = "succeeded"
	if err != nil {
		status.State, status.Message = "failed", err.Error()
	}
	return errors.Join(err, writeUpgradeStatus(dir, status))
}

func installUpgrade(ctx context.Context, cfg Config, dir, component, tag string, writeBinary func(io.Writer) error) error {
	binary := "yagp-daemon"
	if component == "gost" {
		binary = "gost"
	}
	target := filepath.Join(dir, binary)
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("read installed executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("installed executable must be a regular file")
	}
	current, err := executableVersion(ctx, target, component)
	if err != nil {
		return fmt.Errorf("read installed version: %w", err)
	}
	if current == tag {
		return nil
	}
	if !upgrade.IsNewer(component, current, tag) {
		return fmt.Errorf("release %s is not newer than installed version %s", tag, current)
	}
	stage, err := os.MkdirTemp(dir, ".upgrade-*")
	if err != nil {
		return err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	candidate := filepath.Join(stage, binary)
	if err = stageUpgrade(ctx, candidate, component, tag, writeBinary); err != nil {
		return err
	}
	backup := filepath.Join(stage, "previous")
	if err = os.Link(target, backup); err != nil {
		return fmt.Errorf("back up installed executable: %w", err)
	}
	if err = os.Rename(candidate, target); err != nil {
		return fmt.Errorf("replace installed executable: %w", err)
	}
	unit := fmt.Sprintf("yagp-%s-%d.service", component, cfg.NodeID)
	if err = restartAndCheckUpgrade(ctx, cfg, unit, component, tag); err == nil {
		return nil
	}
	if restoreErr := os.Rename(backup, target); restoreErr != nil {
		keepStage = true
		return errors.Join(err, fmt.Errorf("restore executable from %s: %w", backup, restoreErr))
	}
	// The previous binary needs to restart even if the upgrade context was canceled.
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	_ = upgradeCommand(rollbackCtx, "systemctl", "reset-failed", unit)
	if rollbackErr := restartAndCheckUpgrade(rollbackCtx, cfg, unit, component, ""); rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("restart previous version: %w", rollbackErr))
	}
	return fmt.Errorf("upgrade failed and previous version was restored: %w", err)
}

func stageUpgrade(ctx context.Context, path, component, tag string, writeBinary func(io.Writer) error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	err = writeBinary(file)
	if err = errors.Join(err, file.Sync(), file.Close()); err != nil {
		return fmt.Errorf("stage upgrade executable: %w", err)
	}
	version, err := executableVersion(ctx, path, component)
	if err != nil {
		return fmt.Errorf("check staged executable: %w", err)
	}
	if version != tag {
		return fmt.Errorf("staged executable reports version %s, expected %s", version, tag)
	}
	return nil
}

func restartAndCheckUpgrade(ctx context.Context, cfg Config, unit, component, tag string) error {
	restartCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := upgradeCommand(restartCtx, "systemctl", "restart", unit); err != nil {
		return err
	}
	if component == "gost" {
		return WaitForGost(ctx, cfg)
	}
	return waitForDaemon(ctx, cfg, tag)
}

// WaitForGost checks the same authenticated admin endpoint used by the bridge.
func WaitForGost(ctx context.Context, cfg Config) error {
	if !IsLoopbackAddress(cfg.AdminAddress) {
		return errors.New("Gost API address must be loopback")
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return waitForReady(ctx, "Gost API", 10*time.Second, func(ctx context.Context) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.AdminAddress+"/api/config", nil)
		if err != nil {
			return false
		}
		req.SetBasicAuth(cfg.Username, cfg.Password)
		res, err := client.Do(req)
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == http.StatusOK
	})
}

func waitForDaemon(ctx context.Context, cfg Config, tag string) error {
	host, port, err := net.SplitHostPort(cfg.ControlListen)
	if err != nil {
		return err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	cert, err := os.ReadFile(cfg.TLSCertFile)
	if err != nil {
		return err
	}
	tls, err := nodegrpc.NodeClientTLS(string(cert))
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(net.JoinHostPort(host, port), grpc.WithTransportCredentials(tls),
		grpc.WithPerRPCCredentials(nodegrpc.PasswordCredentials(cfg.Username, cfg.Password)))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := nodegrpc.NewNodeClient(conn)
	return waitForReady(ctx, "yagp-daemon control API", 25*time.Second, func(ctx context.Context) bool {
		if tag != "" {
			state, err := client.GetUpgradeStatus(ctx, &emptypb.Empty{}, grpc.WaitForReady(true))
			return err == nil && state.GetSupported() && state.GetDaemonVersion() == tag
		}
		// A rollback can restore a daemon released before the upgrade RPC existed.
		_, err := client.GetObserverEndpoint(ctx, &emptypb.Empty{}, grpc.WaitForReady(true))
		return err == nil
	})
}

func waitForReady(ctx context.Context, service string, timeout time.Duration, probe func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeCtx, stop := context.WithTimeout(ctx, time.Second)
		ready := probe(probeCtx)
		stop()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not become ready: %w", service, ctx.Err())
		case <-ticker.C:
		}
	}
}
