package nodebridge

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDockerBridgeDetectionUsesLocalBridgeDevices(t *testing.T) {
	sysfs := t.TempDir()
	for _, bridge := range []string{"docker0", "br-4d7902c53a23", "br-1234567890ab", "compose-net", "br-lan", "br-bad*name"} {
		if err := os.MkdirAll(filepath.Join(sysfs, bridge, "bridge"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(sysfs, "br-15a9058c9354"), 0700); err != nil {
		t.Fatal(err)
	}
	assertNames := func(reported map[string]struct{}, want []string) {
		t.Helper()
		if got, err := dockerBridgeNames(sysfs, reported); err != nil || !slices.Equal(got, want) {
			t.Fatalf("detected bridge devices = %v, %v, want %v", got, err, want)
		}
	}

	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	socketDir, err := os.MkdirTemp("", "yagp-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "engine.sock")
	assertNames(localDockerBridgeNames(t.Context(), socket), []string{"docker0", "br-1234567890ab", "br-4d7902c53a23"})

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/networks" {
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `[
			{"Name":"bridge","Driver":"bridge"},
			{"Name":"project_default","Id":"4d7902c53a231234","Driver":"bridge"},
			{"Name":"project_custom","Driver":"bridge","Options":{"com.docker.network.bridge.name":"compose-net"}},
			{"Name":"invalid","Driver":"bridge","Options":{"com.docker.network.bridge.name":"br-bad*name"}},
			{"Name":"wrong-driver","Driver":"macvlan","Options":{"com.docker.network.bridge.name":"br-lan"}},
			{"Name":"not-a-live-bridge","Id":"15a9058c93541234","Driver":"bridge"}
		]`)
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()

	assertNames(localDockerBridgeNames(t.Context(), socket), []string{"docker0", "br-4d7902c53a23", "compose-net"})
}
