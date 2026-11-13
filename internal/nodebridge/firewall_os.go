package nodebridge

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strings"
)

const (
	firewallNFT       = "nftables"
	firewallUFW       = "ufw"
	firewallFirewalld = "firewalld"
)

func hostFirewallBackend() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	return readFirewallBackend(os.ReadFile)
}

func readFirewallBackend(readFile func(string) ([]byte, error)) string {
	data, err := readFile("/etc/os-release")
	if errors.Is(err, fs.ErrNotExist) {
		data, err = readFile("/usr/lib/os-release")
	}
	if err != nil {
		return ""
	}
	var distro, related string
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "ID":
			distro = value
		case "ID_LIKE":
			related = value
		}
	}
	families := strings.Fields(related)
	if distro == "ubuntu" || slices.Contains(families, "ubuntu") {
		return firewallUFW
	}
	if isRHELFamily(distro) || slices.ContainsFunc(families, isRHELFamily) {
		return firewallFirewalld
	}
	if distro == "" {
		return ""
	}
	return firewallNFT
}

func isRHELFamily(distro string) bool {
	switch distro {
	case "rhel", "centos", "rocky", "almalinux", "ol", "opencloudos", "opencloudos-stream":
		return true
	default:
		return false
	}
}
