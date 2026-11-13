package nodebridge

import (
	"io/fs"
	"reflect"
	"testing"
)

func TestFirewallBackendUsesOSRelease(t *testing.T) {
	for _, test := range []struct {
		name, etc, fallback, want string
		paths                     []string
	}{
		{"Ubuntu", "NAME=Ubuntu\nID=ubuntu\nID_LIKE=debian", "", firewallUFW, []string{"/etc/os-release"}},
		{"Ubuntu derivative", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"", "", firewallUFW, []string{"/etc/os-release"}},
		{"Debian", "ID=debian", "ID=ubuntu", firewallNFT, []string{"/etc/os-release"}},
		{"Alpine fallback", "", "ID='alpine'", firewallNFT, []string{"/etc/os-release", "/usr/lib/os-release"}},
		{"no OS release", "", "", "", []string{"/etc/os-release", "/usr/lib/os-release"}},
		{"no OS identifier", "NAME=Unknown", "", "", []string{"/etc/os-release"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var paths []string
			got := readFirewallBackend(func(path string) ([]byte, error) {
				paths = append(paths, path)
				data := map[string]string{"/etc/os-release": test.etc, "/usr/lib/os-release": test.fallback}[path]
				if data == "" {
					return nil, fs.ErrNotExist
				}
				return []byte(data), nil
			})
			if got != test.want || !reflect.DeepEqual(paths, test.paths) {
				t.Fatalf("backend = %q via %v, want %q via %v", got, paths, test.want, test.paths)
			}
		})
	}
}

func TestFirewallOptInUsesSelectedBackend(t *testing.T) {
	enabled, disabled := true, false
	for _, test := range []struct {
		name, backend  string
		config         FirewallConfig
		managed, valid bool
	}{
		{"disabled by default", firewallUFW, FirewallConfig{}, false, true},
		{"Ubuntu requires no delegation", firewallUFW, FirewallConfig{Enabled: &enabled}, true, true},
		{"Ubuntu never uses old nft table", firewallUFW, FirewallConfig{Table: "filter", InputChain: "input"}, true, true},
		{"nft delegation still opts in", firewallNFT, FirewallConfig{Table: "filter", InputChain: "input"}, true, true},
		{"explicit opt-out", firewallNFT, FirewallConfig{Enabled: &disabled, Table: "filter", InputChain: "input"}, false, true},
		{"nft requires delegation", firewallNFT, FirewallConfig{Enabled: &enabled}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := newFirewallManager(42, test.config, test.backend)
			status := manager.Status()
			if status.Backend != test.backend || status.Managed != test.managed || (err == nil) != test.valid {
				t.Fatalf("unexpected firewall status: %+v, %v", status, err)
			}
		})
	}
}
