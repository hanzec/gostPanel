package nodebridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNftConfigurationAndQuotaErrors(t *testing.T) {
	const validConfig = "{\"nftables\":[]}"
	ctx := context.Background()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "nft.json")
	cfg := Config{NodeID: 42, ConfigFile: filepath.Join(dir, "node.yaml")}
	policy := trafficQuotaState{LimitBytes: 100, TCPPorts: []uint32{8000}}
	if err := applyNftConfig(ctx, 42, configPath, "invalid JSON"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed config returned %v", err)
	}
	if err := applyNftConfig(ctx, 42, configPath, validConfig); status.Code(err) != codes.Unimplemented {
		t.Fatalf("missing nft returned %v", err)
	}
	if _, err := syncTrafficQuota(ctx, cfg, policy); status.Code(err) != codes.Unimplemented {
		t.Fatalf("missing nft returned %v for quota", err)
	}

	command := filepath.Join(bin, "nft")
	writeUpgradeFixture(t, command, "#!/bin/sh\nprintf '%02100d' 0 >&2\nexit 1\n")
	if err := applyNftConfig(ctx, 42, configPath, validConfig); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unavailable nft backend returned %v", err)
	}
	if _, err := syncTrafficQuota(ctx, cfg, policy); status.Code(err) != codes.FailedPrecondition ||
		!strings.HasSuffix(status.Convert(err).Message(), "…") || len(status.Convert(err).Message()) >= 2100 {
		t.Fatalf("unavailable quota backend did not limit stderr: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := applyNftConfig(canceled, 42, configPath, validConfig); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled config returned %v", err)
	}
	if _, err := syncTrafficQuota(canceled, cfg, policy); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled quota returned %v", err)
	}

	writeUpgradeFixture(t, command, "#!/bin/sh\necho 'invalid JSON'\n")
	if _, err := syncTrafficQuota(ctx, cfg, policy); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("malformed quota backend returned %v", err)
	}
	statePath := trafficQuotaPath(cfg.ConfigFile)
	if err := os.WriteFile(statePath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := syncTrafficQuota(ctx, cfg, policy); status.Code(err) != codes.Internal {
		t.Fatalf("corrupt quota state returned %v", err)
	}
}

func TestNftApplyFailureDoesNotPersistChanges(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	writeUpgradeFixture(t, filepath.Join(bin, "nft"), `#!/bin/sh
case "$1" in
  -c) exit 0 ;;
  -f) echo 'quota replacement failed' >&2; exit 1 ;;
  *) echo 'config replacement failed' >&2; exit 1 ;;
esac
`)
	ctx := context.Background()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "nft.json")
	const previous = "{\"nftables\":[]}\n"
	if err := os.WriteFile(configPath, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyNftConfig(ctx, 42, configPath, strings.TrimSpace(previous)); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("config replacement returned %v", err)
	}
	if got, err := readNftConfig(configPath); err != nil || got != previous {
		t.Fatalf("failed replacement changed saved config to %q, %v", got, err)
	}
	path := filepath.Join(dir, ".traffic-quota.json")
	command := filepath.Join(bin, "nft")
	if err := applyTrafficQuota(ctx, command, path, "yagp_quota_42", trafficQuotaState{LimitBytes: 100}, true); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("quota replacement returned %v", err)
	}
	if state, err := readTrafficQuotaState(path); err != nil || state != nil {
		t.Fatalf("failed replacement persisted quota %v, %v", state, err)
	}
	if err := applyNftConfig(ctx, 42, dir, previous); status.Code(err) != codes.Internal {
		t.Fatalf("staging config over a directory returned %v", err)
	}
	if err := applyTrafficQuota(ctx, command, dir, "yagp_quota_42", trafficQuotaState{LimitBytes: 100}, true); status.Code(err) != codes.Internal {
		t.Fatalf("staging quota over a directory returned %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staged files left behind: %v, %v", entries, err)
	}
}
