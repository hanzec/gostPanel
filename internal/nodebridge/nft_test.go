package nodebridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNftConfigTableScope(t *testing.T) {
	for _, config := range []string{
		`{"nftables":[{"flush":{"ruleset":null}}]}`,
		`{"nftables":[{"add":{"table":{"family":"inet","name":"unmanaged"}}}]}`,
		`{"nftables":[{"add":{"chain":{"family":"ip","table":"yagp_node_1","name":"other"}}}]}`,
		`{"nftables":[{"add":{"chain":{"family":"inet","table":"unmanaged","name":"other"}}}]}`,
		`{"nftables":null}`,
		`{"nftables":[]} {"nftables":[]}`,
	} {
		if _, err := nftBatch(1, config); err == nil {
			t.Errorf("accepted unsupported configuration %s", config)
		}
	}
	if _, err := nftBatch(1, strings.Repeat(" ", nodegrpc.MaxNftConfigBytes+1)); err == nil {
		t.Error("accepted oversized nftables configuration")
	}
}

func TestNftConfigApplyAndRestore(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("YAGP_TEST_NFT") != "1" {
		t.Skip("set YAGP_TEST_NFT=1 in a disposable Linux network namespace with nft installed")
	}
	ctx, cancel := context.WithTimeout(controlTestContext(), 10*time.Second)
	defer cancel()
	id := uint64(time.Now().UnixNano())
	table := fmt.Sprintf("yagp_node_%d", id)
	other := table + "_unmanaged"
	configPath := filepath.Join(t.TempDir(), "nft.json")
	run := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "nft", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("nft %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	run("add", "table", "inet", other)
	t.Cleanup(func() {
		for _, name := range []string{table, other} {
			_ = exec.Command("nft", "delete", "table", "inet", name).Run()
		}
	})
	config := func(chain, hook string) string {
		return fmt.Sprintf(`{"nftables":[{"add":{"chain":{"family":"inet","table":%q,"name":%q,"type":"nat","hook":%q,"prio":-110,"policy":"accept"}}}]}`, table, chain, hook)
	}
	initial := config("before", "prerouting")
	control := controlServer{config: Config{
		NodeID: id, Username: controlTestUser, Password: controlTestPassword, NftConfigFile: configPath,
	}}
	reply, err := control.ApplyNftConfig(ctx, &nodegrpc.NftConfig{Config: initial})
	if err != nil || reply.GetConfig() != initial {
		t.Fatalf("applying nft config through control API returned %v, %v", reply, err)
	}
	if err := applyNftConfig(ctx, id, configPath, config("invalid", "wrong-hook")); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid nft configuration: %v", err)
	}
	if saved, err := readNftConfig(configPath); err != nil || saved != initial {
		t.Fatalf("saved config changed after invalid update: %v", err)
	}
	if got := run("list", "table", "inet", table); !strings.Contains(got, "chain before") {
		t.Fatalf("active config changed after invalid update: %s", got)
	}
	if err := applyNftConfig(ctx, id, configPath, config("after", "prerouting")); err != nil {
		t.Fatal(err)
	}
	if got := run("list", "table", "inet", table); strings.Contains(got, "chain before") || !strings.Contains(got, "chain after") {
		t.Fatalf("active config did not replace the prior config: %s", got)
	}
	run("delete", "table", "inet", table)
	if err := restoreNftConfig(ctx, id, configPath); err != nil {
		t.Fatal(err)
	}
	if got := run("list", "table", "inet", table); !strings.Contains(got, "chain after") {
		t.Fatalf("persisted config not restored: %s", got)
	}
	if err := applyNftConfig(ctx, id, configPath, `{"nftables":[]}`); err != nil {
		t.Fatal(err)
	}
	if tables := run("list", "tables"); strings.Contains(tables, "table inet "+table+"\n") || !strings.Contains(tables, "table inet "+other+"\n") {
		t.Fatalf("clearing the managed table left unexpected tables: %s", tables)
	}
}
