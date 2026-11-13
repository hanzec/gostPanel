package nodebridge

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"strings"
)

func localNodeInterfaceAddresses(ctx context.Context) ([]net.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var dockerBridges map[string]struct{}
	if runtime.GOOS == "linux" {
		dockerBridges = localDockerBridgeNames(ctx, "/var/run/docker.sock")
	}
	var addresses []net.Addr
	for _, iface := range interfaces {
		// Container bridges and a cloud NIC can both use private address ranges.
		if isDockerInterface(iface.Name, dockerBridges) {
			continue
		}
		local, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, local...)
	}
	return addresses, nil
}

func isDockerInterface(name string, bridges map[string]struct{}) bool {
	if _, ok := bridges[name]; ok || name == "docker_gwbridge" || conventionalDockerBridge(name) {
		return true
	}
	// Docker generates host-side veth names with a seven-character hexadecimal suffix.
	id, ok := strings.CutPrefix(name, "veth")
	if !ok || len(id) != 7 || id != strings.ToLower(id) {
		return false
	}
	_, err := strconv.ParseUint(id, 16, 28)
	return err == nil
}
