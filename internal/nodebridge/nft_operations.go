package nodebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *controlServer) GetNftConfig(context.Context, *emptypb.Empty) (*nodegrpc.NftConfig, error) {
	config, err := readNftConfig(s.config.NftConfigFile)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read nftables config: %v", err)
	}
	return &nodegrpc.NftConfig{Config: config}, nil
}

func (s *controlServer) ApplyNftConfig(ctx context.Context, req *nodegrpc.NftConfig) (*nodegrpc.NftConfig, error) {
	if err := applyNftConfig(ctx, s.config.NodeID, s.config.NftConfigFile, req.GetConfig()); err != nil {
		return nil, err
	}
	return req, nil
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
