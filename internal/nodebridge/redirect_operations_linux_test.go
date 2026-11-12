package nodebridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfigureTransparentRedirectSelectsPersistsAndRemoves(t *testing.T) {
	const prepare = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_PREPARE
	const apply = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY
	const remove = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_REMOVE
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	applied := filepath.Join(bin, "applied")
	t.Setenv("YAGP_TEST_NFT_APPLIED", applied)
	nft := filepath.Join(bin, "nft")
	writeUpgradeFixture(t, nft, `#!/bin/sh
case "$1" in
  -j) echo '{"nftables":[{"rule":{"family":"inet","table":"existing","expr":[{"match":{"op":"==","left":{"&":[{"meta":{"key":"mark"}},4278190080]},"right":1493172224}}]}}]}' ;;
  -c) /bin/cat >/dev/null ;;
  -f) /bin/cat >"$YAGP_TEST_NFT_APPLIED" ;;
  *) exit 1 ;;
esac
`)
	control := &controlServer{config: Config{NodeID: 42, ConfigFile: filepath.Join(t.TempDir(), "node.yaml")}}
	path := redirectStatePath(control.config)
	ctx := t.Context()
	req := &nodegrpc.TransparentRedirectRequest{Action: prepare, Owner: "rule-5", Port: 12345, RelayHost: "203.0.113.7"}
	plan, err := control.ConfigureTransparentRedirect(ctx, req)
	if err != nil || plan.GetMark() == 0 || plan.GetMark()&0xff000000 == gost.ClashRedirectMark&0xff000000 || plan.GetActive() {
		t.Fatalf("prepare returned %v, %v", plan, err)
	}
	for _, p := range []string{applied, path} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("prepare wrote %s: %v", p, err)
		}
	}
	req.Action, req.DockerAddress, req.Mark = apply, plan.GetDockerAddress(), gost.ClashRedirectMark
	if _, err := control.ConfigureTransparentRedirect(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("apply conflicting mark returned %v", err)
	}

	req.Mark = plan.GetMark()
	reply, err := control.ConfigureTransparentRedirect(ctx, req)
	if err != nil || !reply.GetActive() {
		t.Fatalf("apply returned %v, %v", reply, err)
	}
	batch, err := os.ReadFile(applied)
	if err != nil || !strings.Contains(string(batch), "203.0.113.7") || !strings.Contains(string(batch), redirectTableComment(redirectRulesVersion, 1)) {
		t.Fatalf("initial batch = %s, %v", batch, err)
	}
	req.RequireActive, req.RelayHost = true, "203.0.113.8"
	if _, err := control.ConfigureTransparentRedirect(ctx, req); err != nil {
		t.Fatal(err)
	}
	state, err := readRedirectState(path)
	if err != nil || state.Revision != 2 || state.Mark != plan.GetMark() || len(state.RelayIPs) != 1 || state.RelayIPs[0].String() != req.RelayHost {
		t.Fatalf("updated state = %+v, %v", state, err)
	}
	if _, err := control.ConfigureTransparentRedirect(ctx, &nodegrpc.TransparentRedirectRequest{
		Action: prepare, Owner: "rule-6", Port: 12346, RelayHost: req.RelayHost,
	}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("preparing second ingress returned %v", err)
	}

	req.Action = remove
	if _, err := control.ConfigureTransparentRedirect(ctx, req); err != nil {
		t.Fatal(err)
	}
	batch, err = os.ReadFile(applied)
	if err != nil || string(batch) != "add table inet yagp_redirect_42\ndelete table inet yagp_redirect_42\n" {
		t.Fatalf("removal batch = %s, %v", batch, err)
	}
	state, err = readRedirectState(path)
	if err != nil || state.Owner != "" || state.Revision != 3 {
		t.Fatalf("removed state = %+v, %v", state, err)
	}
	req.Action = apply
	if _, err := control.ConfigureTransparentRedirect(ctx, req); status.Code(err) != codes.NotFound {
		t.Fatalf("stale sync returned %v", err)
	}
}
