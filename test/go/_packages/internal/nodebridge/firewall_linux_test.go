package nodebridge

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNativeFirewallDelegationAndDrift(t *testing.T) {
	if os.Getenv("YAGP_TEST_NFT") != "1" {
		t.Skip("set YAGP_TEST_NFT=1 in a disposable Linux network namespace with nft installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	nodeID := uint64(time.Now().UnixNano())
	table := fmt.Sprintf("yagp_firewall_test_%d", nodeID)
	chain := fmt.Sprintf("yagp_%d_input", nodeID)
	tcpSet := fmt.Sprintf("yagp_%d_tcp4", nodeID)
	const hostChain = "input"
	const otherChain = "other"

	run := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "nft", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("nft %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	run("add", "table", "inet", table)
	t.Cleanup(func() { _ = exec.Command("nft", "delete", "table", "inet", table).Run() })

	first := netip.MustParseAddr("127.77.42.1")
	second := netip.MustParseAddr("127.77.42.2")
	admin := netip.MustParseAddr("127.77.42.3")
	listen := func(addr netip.Addr, port int) *net.TCPListener {
		t.Helper()
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: addr.AsSlice(), Port: port})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		return listener
	}
	port := uint16(listen(first, 0).Addr().(*net.TCPAddr).Port)
	listen(second, int(port))
	listen(admin, int(port))

	run("add", "chain", "inet", table, hostChain, "{ type filter hook input priority filter; policy drop; }")
	// Leave other addresses in the test namespace to their existing policies.
	run("add", "rule", "inet", table, hostChain, "meta", "nfproto", "ipv6", "accept")
	run("add", "rule", "inet", table, hostChain, "ip", "daddr", "!=", "{", first.String()+",", second.String()+",", admin.String(), "}", "accept")
	run("add", "rule", "inet", table, hostChain, "ip", "daddr", admin.String(), "tcp", "dport", fmt.Sprint(port), "accept", "comment", `"admin access"`)
	run("add", "chain", "inet", table, otherChain)
	run("add", "rule", "inet", table, otherChain, "drop", "comment", `"host-owned rule"`)

	assertConnection := func(address netip.Addr, allowed bool) {
		t.Helper()
		destination := netip.AddrPortFrom(address, port)
		dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}}
		if !allowed {
			dialer.Timeout = 150 * time.Millisecond
		}
		connection, err := dialer.DialContext(ctx, "tcp4", destination.String())
		if connection != nil {
			_ = connection.Close()
		}
		if allowed && err != nil || !allowed && err == nil {
			t.Fatalf("connect to %s with allowed=%t: %v", destination, allowed, err)
		}
	}
	config := FirewallConfig{Table: table, InputChain: hostChain}
	manager, err := newFirewallManager(nodeID, config, firewallNFT)
	if err != nil {
		t.Fatal(err)
	}
	firstPorts := FirewallPorts{TCPAddresses: []FirewallEndpoint{{Address: first, Port: port}}}
	if err := manager.Reconcile(ctx, firstPorts); err == nil || !strings.Contains(err.Error(), "unconditional jump") {
		t.Fatalf("reconcile without delegation: %v", err)
	}
	assertConnection(first, false)
	assertConnection(admin, true)
	run("add", "rule", "inet", table, hostChain, "jump", chain)
	hostBefore := run("-a", "list", "chain", "inet", table, hostChain)
	otherBefore := run("-a", "list", "chain", "inet", table, otherChain)

	assertReconcile := func(manager *FirewallManager, ports FirewallPorts) {
		t.Helper()
		if err := manager.Reconcile(ctx, ports); err != nil {
			t.Fatal(err)
		}
		status := manager.Status()
		if !status.Supported || !status.Managed || !status.Synchronized || status.LastSync == nil || status.LastError != nil {
			t.Fatalf("unexpected firewall status: %+v", status)
		}
	}
	assertReconcile(manager, firstPorts)
	assertConnection(first, true)
	assertConnection(second, false)
	assertConnection(admin, true)

	managedBefore := run("-a", "list", "chain", "inet", table, chain)
	secondPorts := FirewallPorts{TCPAddresses: []FirewallEndpoint{{Address: second, Port: port}}}
	assertReconcile(manager, secondPorts)
	if got := run("-a", "list", "chain", "inet", table, chain); got != managedBefore {
		t.Fatalf("changing set elements replaced managed rules:\n%s\n%s", managedBefore, got)
	}
	assertConnection(first, false)
	assertConnection(second, true)

	var ruleHandle string
	for line := range strings.SplitSeq(managedBefore, "\n") {
		if strings.Contains(line, "@"+tcpSet+" ") {
			_, ruleHandle, _ = strings.Cut(line, "# handle ")
			break
		}
	}
	if ruleHandle == "" {
		t.Fatalf("missing TCP destination rule in %s", managedBefore)
	}
	run("delete", "rule", "inet", table, chain, "handle", ruleHandle)
	assertConnection(second, false)
	// A fresh manager reconstructs rules from persisted desired inputs after a restart.
	restarted, err := newFirewallManager(nodeID, config, firewallNFT)
	if err != nil {
		t.Fatal(err)
	}
	assertReconcile(restarted, secondPorts)
	assertConnection(second, true)
	assertConnection(admin, true)
	if got := run("-a", "list", "chain", "inet", table, hostChain); got != hostBefore {
		t.Fatalf("reconcile changed the host input chain:\n%s\n%s", hostBefore, got)
	}
	if got := run("-a", "list", "chain", "inet", table, otherChain); got != otherBefore {
		t.Fatalf("reconcile changed an unrelated host chain:\n%s\n%s", otherBefore, got)
	}

	// Keep an incompatible set in use outside YAGP's chain so the kernel rejects its replacement.
	udpSet := fmt.Sprintf("yagp_%d_udp", nodeID)
	run("flush", "chain", "inet", table, chain)
	run("delete", "set", "inet", table, udpSet)
	run("add", "set", "inet", table, udpSet, fmt.Sprintf(`{ type inet_service; flags interval; comment "yagp daemon firewall %d"; }`, nodeID))
	run("add", "rule", "inet", table, chain, "ip", "daddr", ".", "tcp", "dport", "@"+tcpSet, "accept")
	run("add", "rule", "inet", table, otherChain, "udp", "dport", "@"+udpSet, "accept")
	prior := run("-a", "list", "table", "inet", table)
	if err := restarted.Reconcile(ctx, firstPorts); err == nil || !strings.Contains(err.Error(), "check firewall update") {
		t.Fatalf("reconcile with incompatible referenced set: %v", err)
	}
	if got := run("-a", "list", "table", "inet", table); got != prior {
		t.Fatalf("failed validation changed the ruleset:\n%s\n%s", prior, got)
	}
	assertConnection(first, false)
	assertConnection(second, true)
	assertConnection(admin, true)
}
