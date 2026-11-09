package handler

import (
	"context"
	"errors"
	"net/http"

	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

func (h *ReportHandler) SessionHTTP(w http.ResponseWriter, r *http.Request) {
	node, err := h.AuthenticateSession(r)
	if err != nil {
		writeNodeHTTPError(w, err)
		return
	}
	stream, err := nodegrpc.AcceptSession(w, r)
	if err != nil {
		return
	}
	defer stream.Close()
	reports := &streamReports{ReportHandler: h, id: uint64(node.ID), node: node}
	session := h.newNodeSession(stream, reports)
	unregister := gost.RegisterNodeStream(node.ID, node.Username, node.Password, session)
	defer unregister()
	defer session.Close()

	// A revoke can commit between authenticating the upgrade and publishing the session.
	ctx, cancel := context.WithTimeout(stream.Context(), connectAuthTimeout)
	_, err = reports.currentNode(ctx)
	cancel()
	if err != nil {
		return
	}
	_ = session.Run()
}

func (h *streamReports) GetNodeConfig(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.NodeConfig, error) {
	node, err := h.currentNode(ctx)
	if err != nil {
		return nil, err
	}
	config, err := yaml.Marshal(struct {
		NodeID             uint   `yaml:"node_id"`
		TrafficLimitBytes  int64  `yaml:"traffic_limit_bytes"`
		TrafficResetPeriod string `yaml:"traffic_reset_period"`
		AutoUpgradeGost    bool   `yaml:"auto_upgrade_gost"`
		AutoUpgradeDaemon  bool   `yaml:"auto_upgrade_daemon"`
	}{
		NodeID: node.ID, TrafficLimitBytes: node.TrafficLimitBytes, TrafficResetPeriod: node.TrafficResetPeriod,
		AutoUpgradeGost: node.UpgradePolicy.AutoUpgradeGost, AutoUpgradeDaemon: node.UpgradePolicy.AutoUpgradeDaemon,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "could not encode node configuration")
	}
	return &nodegrpc.NodeConfig{ConfigYaml: config, NftTemplate: nftTemplate(nodegrpc.NftTableName(h.id))}, nil
}

func (h *streamReports) currentNode(ctx context.Context) (*model.GostNode, error) {
	var node model.GostNode
	if err := h.db.WithContext(ctx).First(&node, h.id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, status.Error(codes.Unauthenticated, "node registration was removed")
		}
		return nil, status.Error(codes.Internal, "could not load node configuration")
	}
	if h.node.SessionGeneration != node.SessionGeneration {
		return nil, status.Error(codes.Unauthenticated, "node credentials were revoked")
	}
	return &node, nil
}
