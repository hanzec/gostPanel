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

func TestNativeFirewalldPolicyAndDrift(t *testing.T) {
	if os.Getenv("YAGP_TEST_FIREWALLD") != "1" {
		t.Skip("set YAGP_TEST_FIREWALLD=1 in a disposable Linux VM with firewalld and a system D-Bus")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	t.Setenv("LC_ALL", "C")
	run := func(command string, args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", command, args, err, output)
		}
		return string(output)
	}

	const busName = "org.fedoraproject.FirewallD1"
	busHasOwner := func() bool {
		t.Helper()
		result := run("dbus-send", "--system", "--print-reply", "--dest=org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.NameHasOwner", "string:"+busName)
		return strings.Contains(result, "boolean true")
	}
	if busHasOwner() {
		t.Fatal("test requires firewalld to be initially stopped in a disposable VM")
	}
	const policy = "yagp-test"
	enabled := true
	config := FirewallConfig{Enabled: &enabled, Policy: policy}
	newManager := func() *FirewallManager {
		t.Helper()
		manager, err := newFirewallManager(42, config, firewallFirewalld)
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	manager := newManager()
	if err := manager.Reconcile(ctx, FirewallPorts{TCP: []uint16{21432}}); err == nil {
		t.Fatal("reconciled against stopped firewalld")
	}
	if status := manager.Status(); status.Backend != firewallFirewalld || status.Supported || !status.Managed ||
		status.Synchronized || status.LastError == nil {
		t.Fatalf("unexpected stopped firewall status: %+v", status)
	}
	if busHasOwner() {
		t.Fatal("reconcile activated stopped firewalld")
	}

	const clientNS = "yagp-firewall-client"
	const ingress = "yagpfw-host"
	const peer = "yagpfw-peer"
	run("ip", "netns", "add", clientNS)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", clientNS).Run() })
	run("ip", "link", "add", ingress, "type", "veth", "peer", "name", peer, "netns", clientNS)
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", ingress).Run() })
	run("ip", "address", "add", "192.0.2.81/24", "dev", ingress)
	run("ip", "address", "add", "192.0.2.82/24", "dev", ingress)
	run("ip", "link", "set", ingress, "up")
	run("ip", "-n", clientNS, "address", "add", "192.0.2.83/24", "dev", peer)
	run("ip", "-n", clientNS, "link", "set", peer, "up")

	if _, err := os.Stat("/run/systemd/system"); err == nil {
		wasEnabled := exec.CommandContext(ctx, "systemctl", "is-enabled", "--quiet", "firewalld.service").Run() == nil
		run("systemctl", "enable", "--now", "firewalld.service")
		t.Cleanup(func() {
			_ = exec.Command("systemctl", "stop", "firewalld.service").Run()
			if !wasEnabled {
				_ = exec.Command("systemctl", "disable", "firewalld.service").Run()
			}
		})
	} else if os.IsNotExist(err) {
		firewalld := exec.Command("firewalld", "--nofork", "--nopid")
		firewalld.Stderr = os.Stderr
		if err := firewalld.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = firewalld.Process.Kill()
			_ = firewalld.Wait()
		})
	} else {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !busHasOwner(); {
		if time.Now().After(deadline) {
			t.Fatal("firewalld did not acquire its D-Bus name")
		}
		time.Sleep(20 * time.Millisecond)
	}

	admin := func(args ...string) string {
		t.Helper()
		return run("firewall-cmd", args...)
	}
	if err := manager.Reconcile(ctx, FirewallPorts{TCP: []uint16{21432}}); err == nil {
		t.Fatal("reconciled without an administrator-provisioned policy")
	}
	admin("--permanent", "--new-policy="+policy)
	t.Cleanup(func() { _ = exec.Command("firewall-cmd", "--permanent", "--delete-policy="+policy).Run() })
	for _, option := range []string{"--set-description=yagp daemon firewall 42", "--set-priority=32767", "--set-target=CONTINUE", "--add-ingress-zone=public", "--add-egress-zone=HOST"} {
		admin("--permanent", "--policy="+policy, option)
	}
	admin("--permanent", "--zone=public", "--add-interface="+ingress)
	t.Cleanup(func() {
		_ = exec.Command("firewall-cmd", "--permanent", "--zone=public", "--remove-interface="+ingress).Run()
	})
	admin("--reload")
	policyPath := "/etc/firewalld/policies/" + policy + ".xml"
	permanent, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}

	listen := func(address string, port int) FirewallEndpoint {
		t.Helper()
		addr := netip.MustParseAddr(address)
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: addr.AsSlice(), Port: port})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		return FirewallEndpoint{Address: addr, Port: uint16(listener.Addr().(*net.TCPAddr).Port)}
	}
	allowed := listen("192.0.2.81", 0)
	unmatched := listen("192.0.2.82", int(allowed.Port))
	assertConnection := func(endpoint FirewallEndpoint, expect bool) {
		t.Helper()
		// The Bash TCP connection runs in a separate namespace so it reaches the public zone.
		command := exec.CommandContext(ctx, "ip", "netns", "exec", clientNS, "timeout", "1", "bash", "-c", `exec 3<>"/dev/tcp/$1/$2"`, "bash", endpoint.Address.String(), fmt.Sprint(endpoint.Port))
		output, err := command.CombinedOutput()
		if (err == nil) != expect {
			t.Fatalf("connection to %s port %d with allowed=%t: %v: %s", endpoint.Address, endpoint.Port, expect, err, output)
		}
	}
	assertConnection(allowed, false)
	desired := FirewallPorts{TCPAddresses: []FirewallEndpoint{allowed}}
	assertSync := func(manager *FirewallManager, ports FirewallPorts) {
		t.Helper()
		if err := manager.Reconcile(ctx, ports); err != nil {
			t.Fatal(err)
		}
		status := manager.Status()
		if status.Backend != firewallFirewalld || !status.Supported || !status.Managed || !status.Synchronized || status.LastSync == nil || status.LastError != nil {
			t.Fatalf("unexpected firewall status: %+v", status)
		}
	}
	assertSync(manager, desired)
	assertConnection(allowed, true)
	assertConnection(unmatched, false)

	rule := strings.TrimSpace(admin("--policy="+policy, "--list-rich-rules"))
	if !strings.Contains(rule, "accept") || strings.Contains(rule, "\n") {
		t.Fatalf("expected one address-scoped accept rule: %q", rule)
	}
	admin("--policy="+policy, "--remove-rich-rule="+rule)
	assertConnection(allowed, false)
	assertSync(manager, desired)
	assertConnection(allowed, true)
	deny := fmt.Sprintf(`rule family="ipv4" destination address="%s" port port="%d" protocol="tcp" reject`, allowed.Address, allowed.Port)
	admin("--zone=public", "--add-rich-rule="+deny)
	assertSync(manager, desired)
	assertConnection(allowed, false)
	admin("--zone=public", "--remove-rich-rule="+deny)
	assertConnection(allowed, true)

	admin("--reload")
	assertConnection(allowed, false)
	restarted := newManager()
	assertSync(restarted, desired)
	assertConnection(allowed, true)
	assertSync(restarted, FirewallPorts{})
	assertConnection(allowed, false)
	if after, err := os.ReadFile(policyPath); err != nil || string(after) != string(permanent) {
		t.Fatalf("YAGP changed the permanent firewalld policy: %v\n%s", err, after)
	}
}
