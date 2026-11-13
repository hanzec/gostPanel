package nodebridge

import (
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestLocalNodeIPsOmitDockerBridgeAddresses(t *testing.T) {
	if os.Getenv("YAGP_TEST_NFT") != "1" {
		t.Skip("set YAGP_TEST_NFT=1 in a disposable Linux network namespace")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("ip link is unavailable")
	}
	const cloudIP = "172.20.231.2"
	const dockerIP = "172.20.232.1"
	const dockerPublicIP = "203.0.113.49"
	for _, fixture := range []struct {
		name string
		cidr []string
	}{
		{"br-af3c1289b341", []string{dockerIP + "/24", dockerPublicIP + "/24"}},
		{"br-yagp-lan", []string{cloudIP + "/24"}},
	} {
		if output, err := exec.CommandContext(t.Context(), ip, "link", "add", fixture.name, "type", "bridge").CombinedOutput(); err != nil {
			t.Skipf("cannot create a bridge in this test namespace: %v (%s)", err, output)
		}
		t.Cleanup(func() {
			if output, err := exec.Command(ip, "link", "delete", fixture.name).CombinedOutput(); err != nil {
				t.Errorf("delete test bridge %s: %v (%s)", fixture.name, err, output)
			}
		})
		for _, cidr := range fixture.cidr {
			if output, err := exec.CommandContext(t.Context(), ip, "addr", "add", cidr, "dev", fixture.name).CombinedOutput(); err != nil {
				t.Fatalf("assign %s to %s: %v (%s)", cidr, fixture.name, err, output)
			}
		}
	}
	addrs, err := localNodeInterfaceAddresses(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const publicIP = "198.51.100.23"
	snapshot := collectNodeIPs(publicIP, "", addrs)
	if !slices.Contains(snapshot.local, cloudIP) {
		t.Errorf("private IP on non-Docker bridge missing: %v", snapshot.local)
	}
	if slices.Contains(snapshot.local, dockerIP) || slices.Contains(snapshot.local, dockerPublicIP) {
		t.Errorf("Docker bridge leaked into local snapshot: %v", snapshot.local)
	}
	if snapshot.ipv4 != publicIP || slices.Contains(snapshot.public, dockerPublicIP) {
		t.Errorf("incorrect public snapshot: IPv4 %q, all %v", snapshot.ipv4, snapshot.public)
	}
}
