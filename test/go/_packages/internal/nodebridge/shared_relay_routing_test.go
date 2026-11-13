package nodebridge

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"yagp/pkg/gost"
)

func TestGostSharedIngressPrioritizesRulesAndRoundsUnmatchedTraffic(t *testing.T) {
	binary := os.Getenv("YAGP_TEST_GOST_BINARY")
	if binary == "" || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") {
		t.Skip("set YAGP_TEST_GOST_BINARY to an installed Linux or macOS Gost binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}

	// Each relay returns a unique byte regardless of the requested target.
	// Keeping the two ports on the same host verifies that bypass ports are honored.
	var relays []*gost.ServiceConfig
	var chains []*gost.ChainConfig
	var addresses []net.Listener
	reserve := func() string {
		t.Helper()
		l := listenLocal(t)
		addresses = append(addresses, l)
		return l.Addr().String()
	}
	for i, marker := range []byte{'A', 'B', 'C'} {
		app := listenLocal(t)
		go func() {
			for {
				conn, err := app.Accept()
				if err != nil {
					return
				}
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var request [1]byte
				if _, err := io.ReadFull(conn, request[:]); err == nil {
					_, _ = conn.Write([]byte{marker})
				}
				_ = conn.Close()
			}
		}()
		address := reserve()
		name := string(marker)
		auth := &gost.AuthConfig{Username: name, Password: "fixture"}
		relays = append(relays, &gost.ServiceConfig{
			Name: name, Addr: address, Listener: &gost.ListenerConfig{Type: "tcp"},
			Handler:   &gost.HandlerConfig{Type: "relay", Auth: auth},
			Forwarder: &gost.ForwarderConfig{Nodes: []*gost.ForwarderNode{{Addr: app.Addr().String(), Network: "tcp"}}},
		})
		chains = append(chains, &gost.ChainConfig{Name: "chain-" + name, Hops: []*gost.HopConfig{{
			Name: "hop-0", Nodes: []*gost.NodeConfig{{Name: "node-" + name, Addr: relays[i].Addr,
				Connector: &gost.ConnectorConfig{Type: "relay", Auth: auth}, Dialer: &gost.DialerConfig{Type: "tcp"}}},
		}}})
	}
	const match = `Bypass("fixture")`
	group := &gost.ChainGroupConfig{
		Chains: []gost.ChainGroupEntry{
			{Chain: chains[0].Name, Matcher: &gost.ChainMatcher{Rule: match}},
			{Chain: chains[1].Name, Matcher: &gost.ChainMatcher{Rule: "!(" + match + ")"}},
			{Chain: chains[2].Name, Matcher: &gost.ChainMatcher{Rule: "!(" + match + ")"}},
		},
		Selector: &gost.SelectorConfig{Strategy: "round"},
	}
	matched, unmatched := reserve(), reserve()
	services := append(relays,
		&gost.ServiceConfig{Name: "matched", Addr: matched, Listener: &gost.ListenerConfig{Type: "tcp"},
			Handler:   &gost.HandlerConfig{Type: "tcp", ChainGroup: group},
			Forwarder: &gost.ForwarderConfig{Nodes: []*gost.ForwarderNode{{Addr: "127.0.0.1:443", Network: "tcp"}}}},
		&gost.ServiceConfig{Name: "unmatched", Addr: unmatched, Listener: &gost.ListenerConfig{Type: "tcp"},
			Handler:   &gost.HandlerConfig{Type: "tcp", ChainGroup: group},
			Forwarder: &gost.ForwarderConfig{Nodes: []*gost.ForwarderNode{{Addr: "127.0.0.1:80", Network: "tcp"}}}},
	)
	cfg, err := json.Marshal(map[string]any{
		"services": services, "chains": chains,
		"bypasses": []map[string]any{{"name": "fixture", "whitelist": true, "matchers": []string{"127.0.0.1:443"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gost.json")
	if err := os.WriteFile(path, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	for _, l := range addresses {
		_ = l.Close()
	}
	cmd := exec.CommandContext(t.Context(), binary, "-C", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", matched, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Gost failed to listen: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	read := func(address string) byte {
		t.Helper()
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte{'?'}); err != nil {
			t.Fatal(err)
		}
		var response [1]byte
		if _, err := io.ReadFull(conn, response[:]); err != nil {
			t.Fatal(err)
		}
		return response[0]
	}
	for range 4 {
		if got := read(matched); got != 'A' {
			t.Fatalf("matched rule selected Relay %q, want A", got)
		}
	}
	first, second := read(unmatched), read(unmatched)
	if (first != 'B' && first != 'C') || (second != 'B' && second != 'C') || first == second ||
		read(unmatched) != first || read(unmatched) != second {
		t.Fatalf("unmatched traffic did not rotate between B and C: %q %q", first, second)
	}
}
