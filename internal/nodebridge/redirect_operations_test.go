package nodebridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRestoreTransparentRedirectChecksAndUpgradesVersion(t *testing.T) {
	initial := redirectState{Version: redirectRulesVersion - 1, Revision: 8, Owner: "rule-5", Port: 12345, Mark: 100}
	var serviceAvailable atomic.Bool
	serviceAvailable.Store(true)
	var dockerMark atomic.Uint32
	dockerMark.Store(initial.Mark)
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			w.WriteHeader(http.StatusOK)
		case "/api/config/services/rule-5":
			if !serviceAvailable.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"data":{"name":"rule-5","addr":"127.0.0.1:12345","sockopts":{"mark":100},"handler":{"type":"red","chain":"tunnel-1-clash-chain"},"listener":{"type":"red"}}}`)
		case "/api/config/services/rule-5-docker":
			fmt.Fprintf(w, `{"data":{"name":"rule-5-docker","addr":"192.0.2.100:12345","sockopts":{"mark":%d},"handler":{"type":"red","chain":"tunnel-1-clash-chain"},"listener":{"type":"red"}}}`, dockerMark.Load())
		default:
			t.Errorf("unexpected Gost request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(admin.Close)
	addr := admin.Listener.Addr().(*net.TCPAddr).String()
	control := &controlServer{config: Config{NodeID: 42, AdminAddress: addr, ConfigFile: filepath.Join(t.TempDir(), "node.yaml")}, client: admin.Client()}
	path := redirectStatePath(control.config)
	save := func(state redirectState) {
		t.Helper()
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save(initial)

	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("YAGP_TEST_NFT_APPLIED", filepath.Join(bin, "applied"))
	t.Setenv("YAGP_TEST_NFT_LIVE", filepath.Join(bin, "live"))
	install := func(ruleset string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, "live"), []byte(ruleset), 0600); err != nil {
			t.Fatal(err)
		}
	}
	install(`{"nftables":[]}`)
	const nft = `#!/bin/sh
case "$1" in
  -j) /bin/cat "$YAGP_TEST_NFT_LIVE" ;;
  -c) /bin/cat >/dev/null ;;
  -f) /bin/cat >"$YAGP_TEST_NFT_APPLIED" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "nft"), []byte(nft), 0700); err != nil {
		t.Fatal(err)
	}

	if err := control.restoreTransparentRedirect(t.Context()); err != nil {
		t.Fatal(err)
	}
	upgraded, err := readRedirectState(path)
	if err != nil || upgraded.Version != redirectRulesVersion || upgraded.Revision != initial.Revision+1 || upgraded.Mark != initial.Mark {
		t.Fatalf("upgraded state = %+v, %v", upgraded, err)
	}
	batch, err := os.ReadFile(filepath.Join(bin, "applied"))
	if err != nil || !strings.Contains(string(batch), redirectTableComment(upgraded.Version, upgraded.Revision)) {
		t.Fatalf("applied rules = %s, %v", batch, err)
	}

	install(fmt.Sprintf(`{"nftables":[{"table":{"family":"inet","name":"yagp_redirect_42","comment":%q}}]}`, redirectTableComment(upgraded.Version, upgraded.Revision)))
	if err := os.Remove(filepath.Join(bin, "applied")); err != nil {
		t.Fatal(err)
	}
	if err := control.restoreTransparentRedirect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "applied")); !os.IsNotExist(err) {
		t.Fatalf("current live rules were reapplied: %v", err)
	}
	install(`{"nftables":[]}`)
	if err := control.restoreTransparentRedirect(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, err := readRedirectState(path)
	if err != nil || restored.Revision != upgraded.Revision {
		t.Fatalf("restoring a missing table changed its revision: %+v, %v", restored, err)
	}
	if err := os.Remove(filepath.Join(bin, "applied")); err != nil {
		t.Fatal(err)
	}
	serviceAvailable.Store(false)
	if err := control.restoreTransparentRedirect(t.Context()); err == nil || !strings.Contains(err.Error(), "Gost transparent service") {
		t.Fatalf("restore without an active Gost listener returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "applied")); !os.IsNotExist(err) {
		t.Fatalf("nft was applied before Gost was available: %v", err)
	}
	serviceAvailable.Store(true)
	withDocker := upgraded
	withDocker.DockerAddress = netip.MustParseAddr("192.0.2.100")
	withDocker.Bridges = []string{"docker0"}
	save(withDocker)
	dockerMark.Store(200)
	if err := control.restoreTransparentRedirect(t.Context()); err == nil || !strings.Contains(err.Error(), "rule-5-docker does not match") {
		t.Fatalf("restore with inconsistent Docker listener returned %v", err)
	}
	dockerMark.Store(initial.Mark)
	if err := control.restoreTransparentRedirect(t.Context()); err != nil {
		t.Fatal(err)
	}
	dockerState, err := readRedirectState(path)
	if err != nil || len(dockerState.Bridges) != 0 || dockerState.Revision != withDocker.Revision+1 {
		t.Fatalf("restore after the saved bridge disappeared = %+v, %v", dockerState, err)
	}
	const foreignMark = `{"rule":{"family":"inet","table":"other","expr":[{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":100}}]}}`
	for _, foreign := range []string{foreignMark, `{"rule":{"family":"inet","table":"gost","expr":[{"redirect":{"port":12345}}]}}`} {
		install(fmt.Sprintf(`{"nftables":[%s,{"table":{"family":"inet","name":"yagp_redirect_42","comment":%q}}]}`,
			foreign, redirectTableComment(dockerState.Version, dockerState.Revision)))
		if err := control.restoreTransparentRedirect(t.Context()); err == nil {
			t.Fatal("restore accepted a conflicting live ruleset")
		}
		batch, err = os.ReadFile(filepath.Join(bin, "applied"))
		if err != nil || string(batch) != "add table inet yagp_redirect_42\ndelete table inet yagp_redirect_42\n" {
			t.Fatalf("suspending a conflicting table applied %s, %v", batch, err)
		}
		unchanged, err := readRedirectState(path)
		if err != nil || !unchanged.sameRules(dockerState) || unchanged.Revision != dockerState.Revision {
			t.Fatalf("suspending changed the saved policy: %+v, %v", unchanged, err)
		}
	}
	install(`{"nftables":[{"rule":{"family":"inet","table":"other","expr":[{"match":{"op":"!=","left":{"meta":{"key":"mark"}},"right":0}}]}}]}`)
	if err := os.Remove(filepath.Join(bin, "applied")); err != nil {
		t.Fatal(err)
	}
	if err := control.restoreTransparentRedirect(t.Context()); err == nil || !strings.Contains(err.Error(), "could not find") {
		t.Fatalf("restoring with all nonzero marks occupied returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "applied")); !os.IsNotExist(err) {
		t.Fatalf("restore touched nft when the owned table was absent: %v", err)
	}
	validAdminAddress := control.config.AdminAddress
	control.config.AdminAddress = "192.0.2.1:8080"
	if err := control.restoreTransparentRedirect(t.Context()); err == nil || !strings.Contains(err.Error(), "must be loopback") {
		t.Fatalf("restore with an invalid Gost admin address returned %v", err)
	}
	control.config.AdminAddress = validAdminAddress

	upgraded.Version++
	save(upgraded)
	if err := control.restoreTransparentRedirect(t.Context()); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("restore with a future rule version returned %v", err)
	}
}

func TestRedirectStateWriteFailuresLeavePreviousPolicy(t *testing.T) {
	dir := t.TempDir()
	nft := filepath.Join(dir, "nft")
	path := filepath.Join(dir, "redirect.json")
	const previous = `{"version":1,"revision":3,"owner":"rule-5","port":12345,"mark":100}`
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := readRedirectState(path)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.Revision++
	for _, test := range []struct{ stage, detail string }{{"-c", "check transparent redirect rules"}, {"-f", "apply transparent redirect rules"}} {
		t.Run(test.stage, func(t *testing.T) {
			if err := os.WriteFile(nft, []byte("#!/bin/sh\n[ \"$1\" != "+test.stage+" ]\n"), 0700); err != nil {
				t.Fatal(err)
			}
			err := writeRedirectState(t.Context(), nft, path, "yagp_redirect_42", old, next)
			if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), test.detail) {
				t.Fatalf("write returned %v", err)
			}
			stored, err := os.ReadFile(path)
			if err != nil || string(stored) != previous {
				t.Fatalf("failed replacement changed the saved policy: %q, %v", stored, err)
			}
		})
	}
	if err := writeRedirectState(t.Context(), nft, dir, "yagp_redirect_42", old, next); status.Code(err) != codes.Internal {
		t.Fatalf("staging on a directory returned %v", err)
	}
	if _, err := readRedirectState(dir); err == nil {
		t.Fatal("state reader accepted a directory")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := applyRedirectBatch(ctx, nft, next.batch("yagp_redirect_42")); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled rule check returned %v", err)
	}
}

func TestRedirectRelayIPsUseIPv4AddressesOnly(t *testing.T) {
	for _, tc := range []struct{ host, expected string }{
		{"::ffff:192.0.2.123", "192.0.2.123"},
		{"2001:db8::1", ""},
		{"localhost", "127.0.0.1"},
	} {
		ips, err := redirectRelayIPs(t.Context(), tc.host)
		if err != nil || (tc.expected == "" && len(ips) != 0) ||
			(tc.expected != "" && (len(ips) != 1 || ips[0].String() != tc.expected)) {
			t.Fatalf("relay %q = %v, %v; want %s", tc.host, ips, err, tc.expected)
		}
	}
	if _, err := redirectRelayIPs(t.Context(), "invalid relay address"); err == nil {
		t.Fatal("invalid Relay address resolved")
	}
}

func TestRedirectStateRestoresRulesIfPersistenceFailsAfterApply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "redirect.json")
	const previous = `{"version":1,"revision":3,"owner":"rule-5","port":12345,"mark":100}`
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := readRedirectState(path)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.Revision++
	next.Port++
	nft := filepath.Join(dir, "nft")
	first := filepath.Join(dir, "applied")
	rollback := filepath.Join(dir, "rollback")
	t.Setenv("YAGP_TEST_NFT_STATE", path)
	t.Setenv("YAGP_TEST_NFT_FIRST", first)
	t.Setenv("YAGP_TEST_NFT_ROLLBACK", rollback)
	const program = `#!/bin/sh
set -eu
if [ "$1" = -c ]; then exit 0; fi
if [ -f "$YAGP_TEST_NFT_FIRST" ]; then
  cat >"$YAGP_TEST_NFT_ROLLBACK"
else
  cat >"$YAGP_TEST_NFT_FIRST"
  mv "$YAGP_TEST_NFT_STATE" "$YAGP_TEST_NFT_STATE.before"
  mkdir "$YAGP_TEST_NFT_STATE"
fi
`
	if err := os.WriteFile(nft, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	const table = "yagp_redirect_42"
	if err := writeRedirectState(t.Context(), nft, path, table, old, next); status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "save transparent redirect state") {
		t.Fatalf("failed rename returned %v", err)
	}
	for name, expected := range map[string][]byte{first: next.batch(table), rollback: old.batch(table)} {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != string(expected) {
			t.Fatalf("batch %s = %s, %v", name, data, err)
		}
	}
}

func TestInspectRedirectTablePreservesUnmanagedRules(t *testing.T) {
	const table = "yagp_redirect_42"
	previous := redirectState{Version: 1, Revision: 3, Owner: "rule-5"}
	foreign := `{"nftables":[{"table":{"family":"inet","name":"yagp_redirect_42","comment":"unmanaged"}}]}`
	if live, err := inspectRedirectTable([]byte(foreign), table, previous, true); live || err == nil {
		t.Fatalf("unmanaged table = %t, %v", live, err)
	}
	const manual = `{"nftables":[{"rule":{"family":"inet","table":"gost","chain":"proxy_tcp","expr":[{"redirect":{"port":12345}}]}}]}`
	if live, err := inspectRedirectTable([]byte(manual), table, redirectState{}, true); live || err == nil {
		t.Fatalf("manual duplicate = %t, %v", live, err)
	}
	if live, err := inspectRedirectTable([]byte(manual), table, previous, false); live || err != nil {
		t.Fatalf("managed removal with a foreign rule = %t, %v", live, err)
	}
	for _, invalid := range []string{`{`, `{"nftables":[{"table":"not an object"}]}`, `{"nftables":[{"rule":"not an object"}]}`} {
		if live, err := inspectRedirectTable([]byte(invalid), table, previous, true); live || err == nil {
			t.Fatalf("invalid nft listing %s = %t, %v", invalid, live, err)
		}
	}
}
