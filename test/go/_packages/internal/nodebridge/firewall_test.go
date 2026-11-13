package nodebridge

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const firewallEmptyHost = `{"nftables":[
  {"table":{"family":"inet","name":"host-filter"}},
  {"chain":{"family":"inet","table":"host-filter","name":"host-input","type":"filter","hook":"input","prio":0,"policy":"drop"}},
  {"rule":{"family":"inet","table":"host-filter","chain":"host-input","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":22}},{"accept":null}]}}
]}`

const firewallLiveHost = `{"nftables":[
  {"table":{"family":"inet","name":"host-filter"}},
  {"chain":{"family":"inet","table":"host-filter","name":"host-input","type":"filter","hook":"input","prio":0,"policy":"drop"}},
  {"rule":{"chain":"host-input","expr":[{"jump":{"target":"yagp_42_input"}}]}},
  {"chain":{"name":"yagp_42_input","comment":"yagp daemon firewall 42"}},
  {"set":{"name":"yagp_42_tcp","type":"inet_service","comment":"yagp daemon firewall 42","elem":[8443,443]}},
  {"set":{"name":"yagp_42_tcp4","type":["ipv4_addr","inet_service"],"comment":"yagp daemon firewall 42","elem":[{"concat":["192.0.2.4",6443]}]}},
  {"set":{"name":"yagp_42_tcp6","type":["ipv6_addr","inet_service"],"comment":"yagp daemon firewall 42"}},
  {"set":{"name":"yagp_42_udp","type":"inet_service","comment":"yagp daemon firewall 42"}},
  {"set":{"name":"yagp_42_udp4","type":["ipv4_addr","inet_service"],"comment":"yagp daemon firewall 42"}},
  {"set":{"name":"yagp_42_udp6","type":["ipv6_addr","inet_service"],"comment":"yagp daemon firewall 42","elem":[{"concat":["2001:db8::6",53]}]}},
  {"rule":{"chain":"yagp_42_input","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":"@yagp_42_tcp"}},{"accept":null}]}},
  {"rule":{"chain":"yagp_42_input","expr":[{"match":{"op":"==","left":{"concat":[{"payload":{"protocol":"ip","field":"daddr"}},{"payload":{"protocol":"tcp","field":"dport"}}]},"right":"@yagp_42_tcp4"}},{"accept":null}]}},
  {"rule":{"chain":"yagp_42_input","expr":[{"match":{"op":"==","left":{"concat":[{"payload":{"protocol":"ip6","field":"daddr"}},{"payload":{"protocol":"tcp","field":"dport"}}]},"right":"@yagp_42_tcp6"}},{"accept":null}]}},
  {"rule":{"chain":"yagp_42_input","expr":[{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":"@yagp_42_udp"}},{"accept":null}]}},
  {"rule":{"chain":"yagp_42_input","expr":[{"match":{"op":"==","left":{"concat":[{"payload":{"protocol":"ip","field":"daddr"}},{"payload":{"protocol":"udp","field":"dport"}}]},"right":"@yagp_42_udp4"}},{"accept":null}]}},
  {"rule":{"chain":"yagp_42_input","expr":[{"match":{"op":"==","left":{"concat":[{"payload":{"protocol":"ip6","field":"daddr"}},{"payload":{"protocol":"udp","field":"dport"}}]},"right":"@yagp_42_udp6"}},{"accept":null}]}}
]}`

func newFirewallTestManager(t *testing.T) *FirewallManager {
	t.Helper()
	manager, err := newFirewallManager(42, FirewallConfig{Table: "host-filter", InputChain: "host-input"}, firewallNFT)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func firewallTestPorts() FirewallPorts {
	return FirewallPorts{
		TCP:          []uint16{443, 8443, 8443},
		TCPAddresses: []FirewallEndpoint{{Address: netip.MustParseAddr("192.0.2.4"), Port: 6443}},
		UDPAddresses: []FirewallEndpoint{{Address: netip.MustParseAddr("2001:db8::6"), Port: 53}},
	}
}

func TestFirewallPlanUpdatesOnlySetElementsForPortChanges(t *testing.T) {
	manager := newFirewallTestManager(t)
	plan := func(ports FirewallPorts) string {
		t.Helper()
		sets, err := manager.desiredSets(ports)
		if err != nil {
			t.Fatal(err)
		}
		live, err := manager.readRuleset([]byte(firewallLiveHost), sets)
		if err != nil || !live.delegated {
			t.Fatalf("read live delegated firewall = %+v, %v", live, err)
		}
		return string(manager.batch(live, sets))
	}
	ports := firewallTestPorts()
	if batch := plan(ports); batch != "" {
		t.Fatalf("unchanged rules produced a batch:\n%s", batch)
	}
	ports.TCP = []uint16{9443, 443}
	ports.TCPAddresses[0].Address = netip.MustParseAddr("192.0.2.5")
	ports.UDPAddresses = nil
	const want = `delete element inet host-filter yagp_42_tcp { 8443 }
add element inet host-filter yagp_42_tcp { 9443 }
delete element inet host-filter yagp_42_tcp4 { 192.0.2.4 . 6443 }
add element inet host-filter yagp_42_tcp4 { 192.0.2.5 . 6443 }
delete element inet host-filter yagp_42_udp6 { 2001:db8::6 . 53 }
`
	if got := plan(ports); got != want {
		t.Fatalf("incremental batch:\n%s\nwant:\n%s", got, want)
	}
}

func TestFirewallPlanRestoresDriftWithoutDeletingDelegatedChain(t *testing.T) {
	manager := newFirewallTestManager(t)
	sets, err := manager.desiredSets(firewallTestPorts())
	if err != nil {
		t.Fatal(err)
	}
	// A changed verdict must be detected even when the number of rules and set elements match.
	listing := strings.Replace(firewallLiveHost, `{"accept":null}`, `{"drop":null}`, 1)
	live, err := manager.readRuleset([]byte(listing), sets)
	if err != nil {
		t.Fatal(err)
	}
	batch := string(manager.batch(live, sets))
	if !strings.HasPrefix(batch, "flush chain inet host-filter yagp_42_input\n") ||
		strings.Count(batch, "add rule") != len(sets) || strings.Contains(batch, "set inet") ||
		strings.Contains(batch, "host-input") || strings.Contains(batch, "table inet") {
		t.Fatalf("unexpected batch for rule drift:\n%s", batch)
	}
}

func TestFirewallRejectsForeignObjectsAndDetectsMissingDelegation(t *testing.T) {
	manager := newFirewallTestManager(t)
	sets, err := manager.desiredSets(FirewallPorts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		listing string
		detail  string
	}{
		{"foreign chain", strings.Replace(firewallLiveHost, `"comment":"yagp daemon firewall 42"`, `"comment":"other owner"`, 1), "conflicts with an existing nftables chain"},
		{"foreign set", strings.Replace(firewallLiveHost, `"name":"yagp_42_tcp","type":"inet_service","comment":"yagp daemon firewall 42"`, `"name":"yagp_42_tcp","type":"inet_service"`, 1), "conflicts with an existing nftables set"},
		{"no hook", strings.Replace(firewallLiveHost, `"hook":"input"`, `"hook":"output"`, 1), "requires existing filter input base chain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.readRuleset([]byte(test.listing), sets); err == nil || !strings.Contains(err.Error(), test.detail) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	for _, listing := range []string{
		firewallEmptyHost,
		strings.Replace(firewallLiveHost, `{"jump":{"target":"yagp_42_input"}}`, `{"match":{}},{"jump":{"target":"yagp_42_input"}}`, 1),
		strings.Replace(firewallLiveHost, `  {"rule":{"chain":"host-input","expr":[{"jump"`, `  {"rule":{"chain":"host-input","expr":[{"drop":null}]}},`+"\n"+`  {"rule":{"chain":"host-input","expr":[{"jump"`, 1),
	} {
		live, err := manager.readRuleset([]byte(listing), sets)
		if err != nil || live.delegated {
			t.Fatalf("absent, conditional or unreachable delegation was accepted: %+v, %v", live, err)
		}
	}
	withCounter := strings.Replace(firewallLiveHost, `{"jump":{"target":"yagp_42_input"}}`, `{"counter":{"packets":0,"bytes":0}},{"jump":{"target":"yagp_42_input"}}`, 1)
	if live, err := manager.readRuleset([]byte(withCounter), sets); err != nil || !live.delegated {
		t.Fatalf("unconditional delegation with a counter was rejected: %+v, %v", live, err)
	}
}

func TestFirewallReconcileChecksBeforeApplyingIdenticalBatch(t *testing.T) {
	manager := newFirewallTestManager(t)
	dir := t.TempDir()
	t.Setenv("YAGP_FIREWALL_TEST_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "live"), []byte(firewallEmptyHost), 0600); err != nil {
		t.Fatal(err)
	}
	nft := filepath.Join(dir, "nft")
	if err := os.WriteFile(nft, []byte(`#!/bin/sh
set -eu
dir="$YAGP_FIREWALL_TEST_DIR"
case "$*" in
  '-j -nn list table inet host-filter') /bin/cat "$dir/live" ;;
  '-c -f -')
    /bin/cat > "$dir/checked"
    if [ -e "$dir/fail-check" ]; then echo 'check error' >&2; exit 1; fi ;;
  '-f -')
    /bin/cat > "$dir/applied"
    if [ -e "$dir/fail-apply" ]; then echo 'apply error' >&2; exit 1; fi ;;
  *) exit 2 ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	failCheck := filepath.Join(dir, "fail-check")
	if err := os.WriteFile(failCheck, nil, 0600); err != nil {
		t.Fatal(err)
	}
	reconcile := func(detail string) {
		t.Helper()
		if err := manager.reconcile(t.Context(), nft, firewallTestPorts()); err == nil || !strings.Contains(err.Error(), detail) {
			t.Fatalf("reconcile error = %v, want %q", err, detail)
		}
	}
	reconcile("check firewall update: check error")
	if _, err := os.Stat(filepath.Join(dir, "applied")); !os.IsNotExist(err) {
		t.Fatalf("applied after validation failure: %v", err)
	}
	if err := os.Remove(failCheck); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fail-apply"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	reconcile("apply firewall update: apply error")
	checked, err := os.ReadFile(filepath.Join(dir, "checked"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(filepath.Join(dir, "applied"))
	if err != nil {
		t.Fatal(err)
	}
	if string(checked) != string(applied) || !strings.Contains(string(applied), "create chain") || strings.Contains(string(applied), "host-input") {
		t.Fatalf("checked batch differs from applied batch or modifies host input chain:\n%s\n%s", checked, applied)
	}
	if err := os.Remove(filepath.Join(dir, "fail-apply")); err != nil {
		t.Fatal(err)
	}
	reconcile("needs an unconditional jump to yagp_42_input")
}

func TestFirewallStatusRecordsConfigError(t *testing.T) {
	manager, err := newFirewallManager(42, FirewallConfig{Table: "filter\nflush ruleset", InputChain: "input"}, firewallNFT)
	if err == nil {
		t.Fatal("accepted invalid config")
	}
	status := manager.Status()
	if !status.Managed || status.Synchronized || status.LastSync != nil || status.LastError == nil || *status.LastError != err.Error() {
		t.Fatalf("unexpected firewall status: %+v", status)
	}
	*status.LastError = "changed snapshot"
	if got := manager.Status(); *got.LastError != err.Error() {
		t.Fatalf("callers can change manager status: %+v", got)
	}
	if reconcileErr := manager.Reconcile(t.Context(), FirewallPorts{}); reconcileErr != err {
		t.Fatalf("reconcile error = %v, want %v", reconcileErr, err)
	}
	data, err := json.Marshal(manager.Status())
	if err != nil || !strings.Contains(string(data), `"last_sync":null`) || !strings.Contains(string(data), `"synchronized":false`) {
		t.Fatalf("unexpected firewall status JSON: %s, %v", data, err)
	}
}

func TestFirewallFailureKeepsLastSuccess(t *testing.T) {
	manager := newFirewallTestManager(t)
	now := time.Now()
	manager.status.Synchronized = true
	manager.status.LastSync = &now
	manager.recordSyncError(os.ErrNotExist)
	status := manager.Status()
	if status.Synchronized || status.LastError == nil || *status.LastError != os.ErrNotExist.Error() || status.LastSync == nil || !status.LastSync.Equal(now) {
		t.Fatalf("unexpected firewall status after failure: %+v", status)
	}
}
