package nodebridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagp/internal/buildinfo"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestUpgradeStatusRPCReadsPersistedOperation(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{NodeID: 42, Username: controlTestUser, Password: controlTestPassword, ConfigFile: filepath.Join(dir, "node.yaml")}
	control := controlServer{config: cfg}
	ctx := t.Context()
	state, err := control.GetUpgradeStatus(ctx, &emptypb.Empty{})
	if err != nil || state.GetState() != "idle" || state.GetDaemonVersion() != buildinfo.Version {
		t.Fatalf("initial status = %v, %v", state, err)
	}
	for _, operation := range []UpgradeStatus{
		{Component: "gost", Tag: "v3.3.2", State: "succeeded"},
		{Component: "gost", Tag: "v3.3.3", State: "failed", Message: "previous version restored"},
	} {
		if err := writeUpgradeStatus(dir, operation); err != nil {
			t.Fatal(err)
		}
		state, err := control.GetUpgradeStatus(ctx, &emptypb.Empty{})
		if err != nil || state.GetComponent() != operation.Component || state.GetTargetVersion() != operation.Tag ||
			state.GetState() != operation.State || state.GetError() != operation.Message {
			t.Fatalf("status after %+v = %v, %v", operation, state, err)
		}
	}
	if err := writeUpgradeStatus(filepath.Join(dir, "missing"), UpgradeStatus{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status write to nonexistent directory returned %v", err)
	}
	path := filepath.Join(dir, upgradeStatusFile)
	if err := os.WriteFile(path, []byte("corrupt status"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.GetUpgradeStatus(ctx, &emptypb.Empty{}); status.Code(err) != codes.Internal {
		t.Fatalf("corrupt status returned %v", err)
	}
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, upgradeStatusFile), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUpgradeStatus(ctx, cfg, filepath.Join(dir, "node.yaml")); err == nil {
		t.Fatal("reading a directory as upgrade status succeeded")
	}
	if err := writeUpgradeStatus(dir, UpgradeStatus{}); err == nil {
		t.Fatal("overwriting a status directory succeeded")
	}
}

func TestUpgradeRejectsInvalidAndUnsupportedRequests(t *testing.T) {
	ctx := t.Context()
	cfg := Config{NodeID: 42, Username: controlTestUser, Password: controlTestPassword}
	control := controlServer{config: cfg}
	if _, err := control.StartUpgrade(ctx, &nodegrpc.NodeUpgradeRequest{Component: "gost", Version: "latest"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid tag returned %v", err)
	}
	if _, err := control.StartUpgrade(ctx, &nodegrpc.NodeUpgradeRequest{Component: "gost", Version: "v3.3.3"}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unconfigured node returned %v", err)
	}
	if err := StartUpgrade(ctx, cfg, "", "gost", "latest"); err == nil || !strings.Contains(err.Error(), "invalid upgrade component") {
		t.Fatalf("invalid StartUpgrade() returned %v", err)
	}
	if err := StartUpgrade(ctx, cfg, "", "gost", "v3.3.3"); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("unsupported StartUpgrade() returned %v", err)
	}
	if err := RunUpgrade(ctx, Config{}, "", "gost", "v3.3.3"); err == nil || !strings.Contains(err.Error(), "invalid node") {
		t.Fatalf("missing node ID returned %v", err)
	}
	if err := RunUpgrade(ctx, cfg, "", "gost", "v3.3.3"); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("unsupported RunUpgrade() returned %v", err)
	}
	if err := UpgradeInstalledDaemon(ctx, cfg, "", strings.NewReader("unused")); err == nil {
		t.Fatal("installer accepted an unconfigured node")
	}
}

func TestUpgradeStagingRejectsUnusableExecutable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "staged")
	wantError := errors.New("download interrupted")
	if err := stageUpgrade(ctx, path, "gost", "v3.3.3", func(io.Writer) error { return wantError }); !errors.Is(err, wantError) {
		t.Fatalf("failed download returned %v", err)
	}
	if err := stageUpgrade(ctx, path, "gost", "v3.3.3", func(io.Writer) error {
		t.Fatal("download ran despite existing staged executable")
		return nil
	}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing staging file returned %v", err)
	}
	path = filepath.Join(t.TempDir(), "staged")
	if err := stageUpgrade(ctx, path, "gost", "v3.3.3", func(w io.Writer) error {
		_, err := io.WriteString(w, "not executable")
		return err
	}); err == nil || !strings.Contains(err.Error(), "check staged executable") {
		t.Fatalf("unusable staged binary returned %v", err)
	}
	dir := t.TempDir()
	cfg := Config{NodeID: 42}
	doNotDownload := func(io.Writer) error {
		t.Fatal("download ran despite missing or unsafe installed executable")
		return nil
	}
	if err := installUpgrade(ctx, cfg, dir, "gost", "v3.3.3", doNotDownload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing installed executable returned %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "gost"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := installUpgrade(ctx, cfg, dir, "gost", "v3.3.3", doNotDownload); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory executable returned %v", err)
	}
	target := filepath.Join(dir, "gost")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf 'unexpected version\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := installUpgrade(ctx, cfg, dir, "gost", "v3.3.3", doNotDownload); err == nil || !strings.Contains(err.Error(), "unexpected Gost version output") {
		t.Fatalf("installed executable with invalid version output returned %v", err)
	}
}

func TestWaitForGostRequiresAuthenticatedReadyResponse(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" || r.Method != "GET" {
			t.Errorf("unexpected readiness request %s %s", r.Method, r.URL.Path)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != controlTestUser || password != controlTestPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()
	cfg := Config{AdminAddress: strings.TrimPrefix(admin.URL, "http://"), Username: controlTestUser, Password: controlTestPassword}
	if err := WaitForGost(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Password = "wrong"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := WaitForGost(ctx, cfg); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unauthorized readiness response returned %v", err)
	}
	cfg.AdminAddress = "192.0.2.1:80"
	if err := WaitForGost(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("nonlocal readiness address returned %v", err)
	}
}

func TestWaitForReadyRetries(t *testing.T) {
	calls := 0
	err := waitForReady(context.Background(), "fixture", 3*time.Second, func(ctx context.Context) bool {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("probe did not receive a deadline")
		}
		calls++
		return calls == 2
	})
	if err != nil || calls != 2 {
		t.Fatalf("readiness retried %d times, err = %v", calls, err)
	}
}

const expectedDaemonUpgradeVersion = "0.1.99-abc1234"

func TestWaitForDaemonUsesUpgradeAndRollbackProbes(t *testing.T) {
	listener := listenLocal(t)
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	ready := health.NewServer()
	ready.SetServingStatus(daemonHealthService+expectedDaemonUpgradeVersion, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, ready)
	go func() { _ = server.Serve(listener) }()
	cfg := Config{ObserverListen: listener.Addr().String()}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := waitForDaemon(ctx, cfg, expectedDaemonUpgradeVersion); err != nil {
		t.Fatalf("upgrade probe: %v", err)
	}
	if err := waitForDaemon(ctx, cfg, ""); err != nil {
		t.Fatalf("rollback probe: %v", err)
	}
	wrongVersion, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if err := waitForDaemon(wrongVersion, cfg, "0.1.98-abc1234"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness accepted a different version: %v", err)
	}
	cfg.ObserverListen = "127.0.0.1:0"
	closedPort, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if err := waitForDaemon(closedPort, cfg, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback readiness accepted a closed port: %v", err)
	}
	cfg.ObserverListen = "192.0.2.1:1234"
	if err := waitForDaemon(ctx, cfg, expectedDaemonUpgradeVersion); err == nil {
		t.Fatal("readiness accepted a nonlocal observer address")
	}
}
