package nodebridge

import (
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestDiscoverDockerBridgesUsesLiveIPv4Address(t *testing.T) {
	if os.Getenv("YAGP_TEST_NFT") != "1" {
		t.Skip("set YAGP_TEST_NFT=1 in a disposable Linux network namespace")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("ip link is unavailable")
	}
	const bridge = "br-594147501234"
	const empty = "br-594147501235"
	for _, name := range []string{bridge, empty} {
		if output, err := exec.CommandContext(t.Context(), ip, "link", "add", name, "type", "bridge").CombinedOutput(); err != nil {
			t.Skipf("cannot create a bridge in this test namespace: %v (%s)", err, output)
		}
		t.Cleanup(func() {
			if output, err := exec.Command(ip, "link", "delete", name).CombinedOutput(); err != nil {
				t.Errorf("delete test bridge %s: %v (%s)", name, err, output)
			}
		})
	}
	if output, err := exec.CommandContext(t.Context(), ip, "addr", "add", "172.30.47.1/24", "dev", bridge).CombinedOutput(); err != nil {
		t.Fatalf("configure test bridge: %v (%s)", err, output)
	}
	bridges, err := discoverDockerBridges(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr("172.30.47.1")
	if !slices.Contains(bridges, dockerBridge{Name: bridge, Address: address}) ||
		slices.ContainsFunc(bridges, func(bridge dockerBridge) bool { return bridge.Name == empty }) {
		t.Fatalf("live Docker bridge discovery = %v", bridges)
	}
	var selected redirectState
	if err := selected.detectDocker(t.Context(), true); err != nil || selected.DockerAddress != bridges[0].Address ||
		!slices.Contains(selected.Bridges, bridge) || slices.Contains(selected.Bridges, empty) {
		t.Fatalf("redirect did not select the live Docker listener: %+v, %v", selected, err)
	}
	if output, err := exec.CommandContext(t.Context(), ip, "addr", "del", "172.30.47.1/24", "dev", bridge).CombinedOutput(); err != nil {
		t.Fatalf("remove Docker gateway address: %v (%s)", err, output)
	}
	persisted := redirectState{DockerAddress: address, Bridges: []string{bridge}}
	if err := persisted.detectDocker(t.Context(), false); err != nil || persisted.DockerAddress != address || len(persisted.Bridges) != 0 {
		t.Fatalf("missing bridge should keep the saved listener and suspend Docker DNAT: %+v, %v", persisted, err)
	}
}
