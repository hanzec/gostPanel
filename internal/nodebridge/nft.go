package nodebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A bridge manages one nftables table. Serialize apply and persistence for that table.
var nftApplyMu sync.Mutex

type nftObject map[string]json.RawMessage
type nftCommand map[string]map[string]nftObject

type nftDocument struct {
	Commands []nftCommand `json:"nftables"`
}

func readNftConfig(configPath string) (string, error) {
	config, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(config), err
}

func restoreNftConfig(ctx context.Context, id uint64, configPath string) error {
	config, err := readNftConfig(configPath)
	if err != nil || config == "" {
		return err
	}
	return updateNft(ctx, id, configPath, config, false)
}

func applyNftConfig(ctx context.Context, id uint64, configPath, config string) error {
	return updateNft(ctx, id, configPath, config, true)
}

func updateNft(ctx context.Context, id uint64, configPath, config string, persist bool) error {
	if runtime.GOOS != "linux" || configPath == "" {
		return status.Error(codes.Unimplemented, "nftables management is unavailable on this node")
	}
	batch, err := nftBatch(id, config)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	nft, err := exec.LookPath("nft")
	if err != nil {
		return status.Error(codes.Unimplemented, "nftables is unavailable: install the nftables package on this node")
	}
	nftApplyMu.Lock()
	defer nftApplyMu.Unlock()
	if err = checkNftBatch(ctx, nft, batch); err != nil {
		return err
	}
	var staged string
	if persist {
		staged, err = stageNftConfig(configPath, config)
		if err != nil {
			return status.Errorf(codes.Internal, "stage nftables config: %v", err)
		}
		defer os.Remove(staged)
	}
	if err = runNft(ctx, nft, batch, "-j", "-f", "-"); err != nil {
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		return status.Errorf(codes.FailedPrecondition, "apply nftables config: %v", err)
	}
	if persist {
		if err = os.Rename(staged, configPath); err != nil {
			return status.Errorf(codes.Internal, "nftables rules applied, but could not save the config: %v", err)
		}
	}
	return nil
}

func nftBatch(id uint64, config string) ([]byte, error) {
	if len(config) > nodegrpc.MaxNftConfigBytes {
		return nil, fmt.Errorf("nftables config exceeds %d bytes", nodegrpc.MaxNftConfigBytes)
	}
	var document nftDocument
	var trailing json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(config))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Commands == nil || decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("expected one JSON object with an nftables command array")
	}
	table := nodegrpc.NftTableName(id)
	for i, command := range document.Commands {
		if err := validateNftCommand(command, table); err != nil {
			return nil, fmt.Errorf("nftables command %d: %w", i+1, err)
		}
	}
	identity := nftObject{"family": json.RawMessage(`"inet"`), "name": json.RawMessage(`"` + table + `"`)}
	addTable := nftCommand{"add": {"table": identity}}
	deleteTable := nftCommand{"delete": {"table": identity}}
	// Add is idempotent. Add then delete handles absent tables on versions without destroy.
	batch := []nftCommand{addTable, deleteTable}
	if len(document.Commands) > 0 {
		batch = append(batch, addTable)
		batch = append(batch, document.Commands...)
	}
	return json.Marshal(nftDocument{Commands: batch})
}

func validateNftCommand(command nftCommand, table string) error {
	objects := command["add"]
	if len(command) != 1 || len(objects) != 1 {
		return errors.New("expected one add command")
	}
	for kind, object := range objects {
		switch kind {
		case "chain", "rule", "set", "map", "element", "counter", "quota", "limit", "flowtable", "ct helper", "ct timeout", "ct expectation", "synproxy":
		default:
			return fmt.Errorf("cannot add a %s object", kind)
		}
		var family, name string
		if json.Unmarshal(object["family"], &family) != nil || json.Unmarshal(object["table"], &name) != nil ||
			family != "inet" || name != table {
			return fmt.Errorf("objects must target table inet %s", table)
		}
	}
	return nil
}

func checkNftBatch(ctx context.Context, nft string, batch []byte) error {
	err := runNft(ctx, nft, batch, "-c", "-j", "-f", "-")
	if err == nil {
		return nil
	}
	// A failing check can also mean the kernel or permissions are unavailable.
	probe := runNft(ctx, nft, nil, "-j", "list", "tables")
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if probe != nil {
		return status.Errorf(codes.FailedPrecondition, "cannot access nftables: %v", probe)
	}
	return status.Errorf(codes.InvalidArgument, "nftables check failed: %v", err)
}

func runNft(ctx context.Context, nft string, batch []byte, args ...string) error {
	_, err := nftOutput(ctx, nft, batch, args...)
	return err
}

func nftOutput(ctx context.Context, nft string, batch []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, nft, args...)
	cmd.Stdin = bytes.NewReader(batch)
	output, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		detail := strings.TrimSpace(string(exit.Stderr))
		if len(detail) > 2048 {
			detail = strings.ToValidUTF8(detail[:2048], "�") + "…"
		}
		if detail != "" {
			return nil, errors.New(detail)
		}
	}
	return output, err
}

func stageNftConfig(path, config string) (string, error) {
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return "", errors.New("nftables config path must be a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".nft-config-*")
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(config)
	if err = errors.Join(err, file.Close()); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}
