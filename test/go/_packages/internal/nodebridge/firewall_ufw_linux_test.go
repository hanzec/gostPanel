package nodebridge

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeUFWDelegationAndDrift(t *testing.T) {
	if os.Getenv("YAGP_TEST_UFW") != "1" {
		t.Skip("set YAGP_TEST_UFW=1 in a disposable Ubuntu VM or container with UFW installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	run := func(command string, args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", command, args, err, output)
		}
		return string(output)
	}
	t.Setenv("LC_ALL", "C")
	if got := run("ufw", "status"); !strings.Contains(got, "Status: inactive") {
		t.Fatalf("test requires an initially inactive UFW in a disposable Ubuntu guest: %s", got)
	}
	const nodeID = 42
	enabled := true
	config := FirewallConfig{Enabled: &enabled}
	newManager := func() *FirewallManager {
		t.Helper()
		manager, err := NewFirewallManager(nodeID, config)
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	manager := newManager()
	inactiveRules := run("iptables-save")
	if err := manager.Reconcile(ctx, FirewallPorts{TCP: []uint16{21432}}); err == nil {
		t.Fatal("reconciled against an inactive UFW")
	}
	if status := manager.Status(); status.Backend != firewallUFW || status.Supported || !status.Managed ||
		status.Synchronized || status.LastError == nil {
		t.Fatalf("unexpected inactive firewall status: %+v", status)
	}
	if got := run("ufw", "status"); !strings.Contains(got, "Status: inactive") {
		t.Fatalf("reconcile activated UFW: %s", got)
	}
	if got := run("iptables-save"); got != inactiveRules {
		t.Fatalf("reconcile changed rules while UFW was inactive:\n%s\n%s", inactiveRules, got)
	}
	run("ufw", "--force", "enable")
	t.Cleanup(func() { _ = exec.Command("ufw", "--force", "disable").Run() })

	listen := func(host string, port int) FirewallEndpoint {
		t.Helper()
		addr := netip.MustParseAddr(host)
		prefix := netip.PrefixFrom(addr, addr.BitLen()).String()
		args := []string{"address", "add", prefix, "dev", "lo"}
		if addr.Is6() {
			args = append(args, "nodad")
		}
		run("ip", args...)
		t.Cleanup(func() { _ = exec.Command("ip", "address", "del", prefix, "dev", "lo").Run() })
		listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: addr.AsSlice(), Port: port})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		return FirewallEndpoint{Address: addr, Port: uint16(listener.Addr().(*net.TCPAddr).Port)}
	}
	allow := listen("192.0.2.71", 0)
	different := listen("192.0.2.72", int(allow.Port))
	admin := listen("192.0.2.73", int(allow.Port))
	deny := listen("192.0.2.74", int(allow.Port))
	allow6 := listen("2001:db8::71", int(allow.Port))
	setAdminRule := func(action string, endpoint FirewallEndpoint) {
		t.Helper()
		args := []string{action, "in", "proto", "tcp", "from", "any", "to", endpoint.Address.String(), "port", fmt.Sprint(endpoint.Port)}
		run("ufw", args...)
		t.Cleanup(func() { _ = exec.Command("ufw", append([]string{"--force", "delete"}, args...)...).Run() })
	}
	setAdminRule("allow", admin)
	setAdminRule("deny", deny)
	removeLoopbackShortcut := func() {
		t.Helper()
		// The isolated guest uses loopback listeners to exercise UFW's input chain.
		run("iptables", "-D", "ufw-before-input", "-i", "lo", "-j", "ACCEPT")
		run("ip6tables", "-D", "ufw6-before-input", "-i", "lo", "-j", "ACCEPT")
	}
	removeLoopbackShortcut()
	adminRulesPath := "/etc/ufw/user.rules"
	adminRules, err := os.ReadFile(adminRulesPath)
	if err != nil {
		t.Fatal(err)
	}
	assertConnection := func(target FirewallEndpoint, allowed bool) {
		t.Helper()
		destination := netip.AddrPortFrom(target.Address, target.Port)
		local := net.IPv4(127, 0, 0, 1)
		if target.Address.Is6() {
			local = net.IPv6loopback
		}
		dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: local}}
		if !allowed {
			dialer.Timeout = 150 * time.Millisecond
		}
		connection, err := dialer.DialContext(ctx, "tcp", destination.String())
		if connection != nil {
			_ = connection.Close()
		}
		if allowed && err != nil || !allowed && err == nil {
			t.Fatalf("connect to %s with allowed=%t: %v", destination, allowed, err)
		}
	}

	assertSync := func(manager *FirewallManager, desired FirewallPorts) {
		t.Helper()
		if err := manager.Reconcile(ctx, desired); err != nil {
			t.Fatal(err)
		}
		status := manager.Status()
		if status.Backend != firewallUFW || !status.Supported || !status.Managed || !status.Synchronized || status.LastSync == nil || status.LastError != nil {
			t.Fatalf("unexpected firewall status: %+v", status)
		}
	}
	assertConnection(allow, false)
	assertConnection(allow6, false)
	assertConnection(admin, true)
	desired := FirewallPorts{TCPAddresses: []FirewallEndpoint{allow, admin, deny, allow6}}
	assertSync(manager, desired)
	assertConnection(allow, true)
	assertConnection(allow6, true)
	assertConnection(different, false)
	assertConnection(admin, true)
	assertConnection(deny, false)

	const userChain = "ufw-user-input"
	var ruleNumber, ownedRule int
	for line := range strings.SplitSeq(run("iptables", "-S", userChain), "\n") {
		if !strings.HasPrefix(line, "-A "+userChain+" ") {
			continue
		}
		ruleNumber++
		if strings.Contains(line, allow.Address.String()+"/32") && strings.Contains(line, "yagp daemon firewall 42") {
			ownedRule = ruleNumber
			break
		}
	}
	if ownedRule == 0 {
		t.Fatal("missing YAGP-owned allowance in UFW's user input chain")
	}
	run("iptables", "-D", userChain, strconv.Itoa(ownedRule))
	assertConnection(allow, false)
	assertSync(manager, desired)
	assertConnection(allow, true)
	// A host deny added after an existing allowance must take precedence at the next sync.
	hostDeny := []string{userChain, "-d", allow.Address.String() + "/32", "-p", "tcp", "--dport", strconv.Itoa(int(allow.Port)), "-j", "DROP"}
	run("iptables", append([]string{"-A"}, hostDeny...)...)
	assertSync(manager, desired)
	assertConnection(allow, false)
	run("iptables", append([]string{"-D"}, hostDeny...)...)
	assertSync(manager, desired)
	assertConnection(allow, true)

	run("ufw", "reload")
	removeLoopbackShortcut()
	assertConnection(allow, false)
	assertConnection(allow6, false)
	restarted := newManager()
	assertSync(restarted, desired)
	assertConnection(allow, true)
	assertConnection(allow6, true)
	assertSync(restarted, FirewallPorts{})
	assertConnection(allow, false)
	assertConnection(allow6, false)
	assertConnection(admin, true)
	assertConnection(deny, false)
	if after, err := os.ReadFile(adminRulesPath); err != nil || !bytes.Equal(adminRules, after) {
		t.Fatalf("YAGP changed persistent UFW administrator rules: %v\n%s", err, after)
	}
}
