package nodebridge

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTrafficQuotaBatch(t *testing.T) {
	const table = "yagp_quota_42"
	batch := string(trafficQuotaBatch(table, trafficQuotaState{
		LimitBytes: 100, UsedBytes: 120, TCPPorts: []uint32{8080, 8443}, UDPPorts: []uint32{8080},
	}))
	if !strings.Contains(batch, "add quota inet yagp_quota_42 traffic { over 100 bytes used 100 bytes; }\n") {
		t.Fatalf("batch did not seed the kernel quota at its limit:\n%s", batch)
	}
	var rules []string
	for _, line := range strings.Split(batch, "\n") {
		if strings.HasPrefix(line, "add rule ") {
			rules = append(rules, line)
		}
	}
	want := []string{
		`add rule inet yagp_quota_42 ingress tcp dport { 8080, 8443 } quota name "traffic" drop`,
		`add rule inet yagp_quota_42 egress tcp sport { 8080, 8443 } quota name "traffic" drop`,
		`add rule inet yagp_quota_42 ingress udp dport { 8080 } quota name "traffic" drop`,
		`add rule inet yagp_quota_42 egress udp sport { 8080 } quota name "traffic" drop`,
	}
	if !slices.Equal(rules, want) {
		t.Fatalf("scoped quota rules = %v, want %v", rules, want)
	}
	cleared := string(trafficQuotaBatch(table, trafficQuotaState{TCPPorts: []uint32{8080}}))
	if want := "add table inet yagp_quota_42\ndelete table inet yagp_quota_42\n"; cleared != want {
		t.Fatalf("clearing a quota must remove only its own table, got %q", cleared)
	}
}

func TestTrafficQuotaStateFile(t *testing.T) {
	ctx := context.Background()
	cfg := Config{NodeID: 42, ConfigFile: filepath.Join(t.TempDir(), "node.yaml")}
	path := trafficQuotaPath(cfg.ConfigFile)
	if err := restoreTrafficQuota(ctx, cfg); err != nil {
		t.Fatalf("restore without a quota file: %v", err)
	}

	active := trafficQuotaState{LimitBytes: 100, UsedBytes: 120, WindowStart: 42, TCPPorts: []uint32{8080}}
	if err := applyTrafficQuota(ctx, "", path, "yagp_quota_42", active, false); err != nil {
		t.Fatal(err)
	}
	if saved, err := readTrafficQuotaState(path); err != nil || !reflect.DeepEqual(saved, &active) {
		t.Fatalf("saved quota = %+v, %v, want %+v", saved, err, active)
	}

	if err := applyTrafficQuota(ctx, "", path, "yagp_quota_42", trafficQuotaState{WindowStart: -1}, false); err != nil {
		t.Fatal(err)
	}
	if err := restoreTrafficQuota(ctx, cfg); err != nil {
		t.Fatalf("restore a disabled quota: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"limit_bytes":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restoreTrafficQuota(ctx, cfg); err == nil {
		t.Fatal("restore accepted a truncated quota file")
	}
}

func TestTrafficQuotaRejectsInvalidPolicy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("traffic quotas are supported on Linux")
	}
	cfg := Config{NodeID: 42, ConfigFile: filepath.Join(t.TempDir(), "node.yaml")}
	for _, test := range []struct {
		name   string
		policy trafficQuotaState
	}{
		{"limit exceeds kernel range", trafficQuotaState{LimitBytes: math.MaxInt64 + 1}},
		{"active quota with a negative window", trafficQuotaState{LimitBytes: 100, WindowStart: -1}},
		{"TCP port zero", trafficQuotaState{LimitBytes: 100, TCPPorts: []uint32{0}}},
		{"UDP port overflow", trafficQuotaState{LimitBytes: 100, UDPPorts: []uint32{65536}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := syncTrafficQuota(context.Background(), cfg, test.policy); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid policy returned %v, want InvalidArgument", err)
			}
		})
	}
}

func TestNativeTrafficQuotaLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("YAGP_TEST_NFT") != "1" {
		t.Skip("set YAGP_TEST_NFT=1 in a disposable Linux network namespace with nft installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nft, err := exec.LookPath("nft")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{NodeID: uint64(time.Now().UnixNano()), ConfigFile: filepath.Join(t.TempDir(), "node.yaml")}
	table := fmt.Sprintf("yagp_quota_%d", cfg.NodeID)
	t.Cleanup(func() { _ = exec.Command(nft, "delete", "table", "inet", table).Run() })

	tcp := listenLocal(t)
	port := uint32(tcp.Addr().(*net.TCPAddr).Port)
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	apply := func(policy trafficQuotaState) uint64 {
		t.Helper()
		used, err := syncTrafficQuota(ctx, cfg, policy)
		if err != nil {
			t.Fatal(err)
		}
		return used
	}
	live := func() *liveTrafficQuota {
		t.Helper()
		quota, err := readLiveTrafficQuota(ctx, nft, table)
		if err != nil {
			t.Fatal(err)
		}
		return quota
	}

	state := trafficQuotaState{
		LimitBytes: 100_000, UsedBytes: 20, WindowStart: 100,
		TCPPorts: []uint32{port, port}, UDPPorts: []uint32{port},
	}
	if used := apply(state); used != 20 {
		t.Fatalf("initial usage = %d, want 20", used)
	}
	clientTCP, err := net.DialTimeout("tcp4", tcp.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer clientTCP.Close()
	serverTCP, err := tcp.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer serverTCP.Close()
	clientUDP, err := net.DialTimeout("udp4", udp.LocalAddr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer clientUDP.Close()
	checkQuotaPacket(t, clientTCP, serverTCP, true)
	checkQuotaPacket(t, clientUDP, udp, true)
	counted := apply(state)
	if counted <= 20 {
		t.Fatalf("kernel did not account for traffic, usage = %d", counted)
	}

	state.LimitBytes, state.UDPPorts = 10, nil
	if used := apply(state); used < counted {
		t.Fatalf("lowering the limit discarded usage, got %d, want at least %d", used, counted)
	}
	checkQuotaPacket(t, clientTCP, serverTCP, false)
	checkQuotaPacket(t, serverTCP, clientTCP, false)
	checkQuotaPacket(t, clientUDP, udp, true)

	state.TCPPorts, state.UDPPorts = nil, []uint32{port}
	apply(state)
	checkQuotaPacket(t, clientUDP, udp, false)
	state.WindowStart, state.LimitBytes, state.UsedBytes = 200, 100_000, 0
	if used := apply(state); used != 0 {
		t.Fatalf("new window carried over %d bytes", used)
	}
	checkQuotaPacket(t, clientUDP, udp, true)

	state.UsedBytes = 5_000
	if used := apply(state); used != 5_000 {
		t.Fatalf("usage after applying recovery floor = %d, want 5000", used)
	}
	if err := runNft(ctx, nft, nil, "delete", "table", "inet", table); err != nil {
		t.Fatal(err)
	}
	if err := restoreTrafficQuota(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if quota := live(); quota == nil || quota.Used != 5_000 {
		t.Fatalf("restored kernel quota = %+v, want 5000 used bytes", quota)
	}
	if used := apply(trafficQuotaState{WindowStart: -1, UsedBytes: 5_000}); used != 0 {
		t.Fatalf("clearing quota retained %d bytes", used)
	}
	if quota := live(); quota != nil {
		t.Fatalf("clearing quota left a kernel quota: %+v", quota)
	}
}

func checkQuotaPacket(t *testing.T, sender, receiver net.Conn, allowed bool) {
	t.Helper()
	const payload = "quota"
	if err := receiver.SetReadDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(sender, payload); err != nil {
		t.Fatal(err)
	}
	var packet [len(payload)]byte
	_, err := io.ReadFull(receiver, packet[:])
	if allowed {
		if err != nil || string(packet[:]) != payload {
			t.Fatalf("quota blocked allowed traffic: %q, %v", packet, err)
		}
		return
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("expected blocked traffic to time out, got %q, %v", packet, err)
	}
}
