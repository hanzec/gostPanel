package nodebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *controlServer) GetNftConfig(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.NftConfig, error) {
	if runtime.GOOS != "linux" {
		return nil, status.Error(codes.Unimplemented, "nftables is only available on Linux")
	}
	nft, err := exec.LookPath("nft")
	if err != nil {
		return nil, status.Error(codes.Unimplemented, "nftables is unavailable: install the nftables package on this node")
	}
	table, legacy := nftTables(s.config.NodeID, s.config.NftConfigFile)
	quota, legacyQuota := trafficQuotaTables(s.config)
	redirect := nodegrpc.TransparentRedirectTableName(s.config.NodeID)
	config, err := readLiveNftConfig(ctx, nft, []string{table, legacy, quota, legacyQuota, redirect})
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Errorf(codes.FailedPrecondition, "read nftables rules: %v", err)
	}
	// Older panels expect the saved add commands in Config.
	saved, _ := readNftConfig(s.config.NftConfigFile)
	return &nodegrpc.NftConfig{Config: renameNftConfig(saved, legacy, table), Table: table, LiveConfig: config}, nil
}

func readLiveNftConfig(ctx context.Context, nft string, managedTables []string) (string, error) {
	nftApplyMu.Lock()
	defer nftApplyMu.Unlock()
	output, err := nftOutput(ctx, nft, nil, "-j", "list", "tables")
	if err != nil {
		return "", err
	}
	var tables struct {
		Objects []struct {
			Table struct {
				Family string `json:"family"`
				Name   string `json:"name"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(output, &tables); err != nil {
		return "", err
	}

	type listing struct {
		Objects []nftObject `json:"nftables"`
	}
	result := listing{Objects: []nftObject{}}
	for _, object := range tables.Objects {
		table := object.Table
		if table.Family != "inet" || !slices.Contains(managedTables, table.Name) {
			continue
		}
		output, err = nftOutput(ctx, nft, nil, "-j", "list", "table", table.Family, table.Name)
		if err != nil {
			return "", err
		}
		var rules listing
		if err := json.Unmarshal(output, &rules); err != nil {
			return "", err
		}
		for _, rule := range rules.Objects {
			if _, metadata := rule["metainfo"]; !metadata {
				result.Objects = append(result.Objects, rule)
			}
		}
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	return string(encoded), err
}

func (s *controlServer) ApplyNftConfig(ctx context.Context, req *nodegrpc.NftConfig) (*nodegrpc.NftConfig, error) {
	if err := applyNftConfig(ctx, s.config.NodeID, s.config.NftConfigFile, req.GetConfig()); err != nil {
		return nil, err
	}
	return s.nftConfigReply(req.GetConfig()), nil
}

func (s *controlServer) nftConfigReply(config string) *nodegrpc.NftConfig {
	table, legacy := nftTables(s.config.NodeID, s.config.NftConfigFile)
	return &nodegrpc.NftConfig{Config: renameNftConfig(config, legacy, table), Table: table}
}

func renameNftConfig(config, from, to string) string {
	if from != "" && config != "" {
		if doc, err := parseNftConfig(config); err == nil && doc.renameTable(from, to) {
			if renamed, err := json.MarshalIndent(doc, "", "  "); err == nil {
				return string(renamed)
			}
		}
	}
	return config
}

const panelConfigCache = "panel-config.pb"

func storePanelConfig(configPath string, config *nodegrpc.NodeConfig) error {
	data, err := proto.Marshal(config)
	if err != nil {
		return err
	}
	return WriteNodeFile(filepath.Join(filepath.Dir(configPath), panelConfigCache), data)
}

// ReadNftTemplate uses the snapshot fetched by the daemon's established session.
func ReadNftTemplate(configPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), panelConfigCache))
	if err != nil {
		return "", fmt.Errorf("read node configuration cached by the daemon: %w", err)
	}
	config := new(nodegrpc.NodeConfig)
	if err = proto.Unmarshal(data, config); err != nil {
		return "", fmt.Errorf("decode cached node configuration: %w", err)
	}
	if config.GetNftTemplate() == "" {
		return "", errors.New("cached node configuration has no nftables template")
	}
	return config.GetNftTemplate(), nil
}

func ReadNodeNftTemplate(configPath string, id uint64) (string, error) {
	template, err := ReadNftTemplate(configPath)
	table, legacy := nftTables(id, configPath)
	return renameNftConfig(template, legacy, table), err
}
