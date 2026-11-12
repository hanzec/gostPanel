package nodebridge

import (
	"context"
	"encoding/json"
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
	"google.golang.org/protobuf/types/known/emptypb"
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
		if _, err := nftBatch(nodegrpc.NftTableName(1), "", config); err == nil {
			t.Errorf("accepted unsupported configuration %s", config)
		}
	}
	if _, err := nftBatch(nodegrpc.NftTableName(1), "", strings.Repeat(" ", nodegrpc.MaxNftConfigBytes+1)); err == nil {
		t.Error("accepted oversized nftables configuration")
	}
}

func TestNftConfigMigratesOwnedLegacyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nft.json")
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), runtimeLayoutFile), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	table, legacy := nftTables(42, path)
	config := `{"nftables":[{"add":{"chain":{"family":"inet","table":"yagp_node_42","name":"yagp_node_42"}}}]}`
	batch, err := nftBatch(table, legacy, config)
	if err != nil {
		t.Fatal(err)
	}
	var doc nftDocument
	if err := json.Unmarshal(batch, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Commands) != 6 || string(doc.Commands[1]["delete"]["table"]["name"]) != `"yagp_node_42"` ||
		string(doc.Commands[5]["add"]["chain"]["table"]) != `"yagp_node"` || string(doc.Commands[5]["add"]["chain"]["name"]) != `"yagp_node_42"` {
		t.Fatalf("unexpected migration batch: %s", batch)
	}
	if _, err := nftBatch(table, legacy, strings.Replace(config, `"table":"yagp_node_42"`, `"table":"yagp_node_43"`, 1)); err == nil {
		t.Fatal("accepted another node's table")
	}
	control := controlServer{config: Config{NodeID: 42, NftConfigFile: path}}
	reply := control.nftConfigReply(config)
	if reply.Table != nodegrpc.SingleNodeNftTable || !strings.Contains(reply.Config, `"table": "yagp_node"`) {
		t.Fatalf("editor did not receive the migrated table: %v", reply)
	}
	if err := storePanelConfig(path, &nodegrpc.NodeConfig{NftTemplate: config}); err != nil {
		t.Fatal(err)
	}
	if template, err := ReadNodeNftTemplate(path, 42); err != nil || template != reply.Config {
		t.Fatalf("cached template did not migrate with the editor configuration: %q, %v", template, err)
	}
}

func TestNftConfigApplyAndRestore(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("YAGP_TEST_NFT") != "1" {
		t.Skip("set YAGP_TEST_NFT=1 in a disposable Linux network namespace with nft installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
		for _, name := range []string{table, other, nodegrpc.SingleNodeNftTable} {
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
	empty, err := control.GetNftConfig(ctx, &emptypb.Empty{})
	if err != nil || empty.GetLiveConfig() != "{\n  \"nftables\": []\n}" || empty.GetConfig() != "" {
		t.Fatalf("preview of a node without managed tables returned %v, %v", empty, err)
	}
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
	preview, err := control.GetNftConfig(ctx, &emptypb.Empty{})
	if err != nil || preview.GetConfig() != initial || !strings.Contains(preview.GetLiveConfig(), `"name": "before"`) ||
		strings.Contains(preview.GetLiveConfig(), other) {
		t.Fatalf("preview did not show the node's active table alone: %v, %v", preview, err)
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
	if err := WriteNodeFile(filepath.Join(filepath.Dir(configPath), runtimeLayoutFile), []byte("1\n")); err != nil {
		t.Fatal(err)
	}
	if err := restoreNftConfig(ctx, id, configPath); err != nil {
		t.Fatal(err)
	}
	if got := run("list", "table", "inet", nodegrpc.SingleNodeNftTable); !strings.Contains(got, "chain after") {
		t.Fatalf("legacy config did not migrate: %s", got)
	}
	if tables := run("list", "tables"); strings.Contains(tables, "table inet "+table+"\n") {
		t.Fatalf("migration left the legacy table: %s", tables)
	}
	if err := applyNftConfig(ctx, id, configPath, `{"nftables":[]}`); err != nil {
		t.Fatal(err)
	}
	if tables := run("list", "tables"); strings.Contains(tables, "table inet "+nodegrpc.SingleNodeNftTable+"\n") || !strings.Contains(tables, "table inet "+other+"\n") {
		t.Fatalf("clearing the managed table left unexpected tables: %s", tables)
	}
}
