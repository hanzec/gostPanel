package nodebridge

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRestoreTransparentRedirectChecksAndUpgradesVersion(t *testing.T) {
	initial := redirectState{Version: redirectRulesVersion - 1, Revision: 8, Owner: "rule-5", Port: 12345, Mark: 100}
	var serviceAvailable atomic.Bool
	serviceAvailable.Store(true)
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

	upgraded.Version++
	save(upgraded)
	if err := control.restoreTransparentRedirect(t.Context()); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("restore with a future rule version returned %v", err)
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
}
