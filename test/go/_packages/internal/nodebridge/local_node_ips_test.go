package nodebridge

import "testing"

func TestDockerInterfaceNameFiltering(t *testing.T) {
	bridges := map[string]struct{}{"compose-net": {}}
	for _, test := range []struct {
		name   string
		docker bool
	}{
		{"docker0", true},
		{"docker_gwbridge", true},
		{"br-4d7902c53a23", true},
		{"veth2fa0a17", true},
		{"compose-net", true},
		{"ens5", false},
		{"eth0", false},
		{"br-lan", false},
		{"br-1234567890xy", false},
		{"veth-office", false},
	} {
		if got := isDockerInterface(test.name, bridges); got != test.docker {
			t.Errorf("isDockerInterface(%q) = %v, want %v", test.name, got, test.docker)
		}
	}
}
