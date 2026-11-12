package nodebridge

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerBridgeDiscoveryHandlesUnavailableNetworkState(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if bridges, err := discoverDockerBridges(ctx); len(bridges) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery returned %v, %v", bridges, err)
	}

	sysfs := t.TempDir()
	if bridges, err := dockerBridgeNames(filepath.Join(sysfs, "missing"), nil); len(bridges) != 0 || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing interface directory returned %v, %v", bridges, err)
	}
	bridge := filepath.Join(sysfs, "docker0")
	if err := os.Mkdir(bridge, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bridge", filepath.Join(bridge, "bridge")); err != nil {
		t.Fatal(err)
	}
	if bridges, err := dockerBridgeNames(sysfs, nil); len(bridges) != 0 || err == nil || !strings.Contains(err.Error(), "inspect network interface docker0") {
		t.Fatalf("unreadable bridge metadata returned %v, %v", bridges, err)
	}
}

func TestLocalDockerBridgeNamesFallsBackForBadAPIResponses(t *testing.T) {
	socketDir, err := os.MkdirTemp("", "yagp-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	for _, test := range []struct {
		name string
		code int
		body string
	}{
		{"redirect", http.StatusFound, ""},
		{"unavailable", http.StatusServiceUnavailable, ""},
		{"invalid-json", http.StatusOK, "invalid JSON"},
		{"invalid-id", http.StatusOK, `[{"Name":"custom","Id":"invalidID0001234","Driver":"bridge"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := filepath.Join(socketDir, test.name)
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/networks" {
					t.Errorf("followed Docker API redirect to %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if test.code == http.StatusFound {
					w.Header().Set("Location", "/other-endpoint")
				}
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			})}
			t.Cleanup(func() { _ = server.Close() })
			go func() { _ = server.Serve(listener) }()
			got := localDockerBridgeNames(t.Context(), socket)
			if test.name == "invalid-id" {
				if got == nil || len(got) != 0 {
					t.Fatalf("valid API response without usable networks returned %v", got)
				}
			} else if got != nil {
				t.Fatalf("bad Docker response returned %v rather than requesting interface fallback", got)
			}
		})
	}
}

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
