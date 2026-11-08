package repository

import (
	"yagp/internal/model"

	"gorm.io/gorm"
)

type TrafficTotals struct {
	InputBytes  int64 `json:"input_bytes"`
	OutputBytes int64 `json:"output_bytes"`
}

type TunnelExitTraffic struct {
	ID             uint               `json:"id"`
	Name           string             `json:"name"`
	Status         model.TunnelStatus `json:"status"`
	ExitNodeID     uint               `json:"exit_node_id"`
	ExitNodeName   string             `json:"exit_node_name"`
	ExitNodeStatus model.NodeStatus   `json:"exit_node_status"`
	TrafficTotals
	Measured bool `json:"measured"`
}

func (r *TunnelRepository) ExitTraffic() ([]TunnelExitTraffic, error) {
	traffic := make([]TunnelExitTraffic, 0)
	err := r.DB.Model(&model.GostTunnel{}).
		Select(`tunnels.id, tunnels.name, tunnels.status, tunnels.exit_node_id,
			nodes.name AS exit_node_name, nodes.status AS exit_node_status,
			tunnels.relay_input_bytes AS input_bytes, tunnels.relay_output_bytes AS output_bytes,
			(tunnels.relay_service_created_at > 0) AS measured`).
		Joins("JOIN nodes ON nodes.id = tunnels.exit_node_id AND nodes.deleted_at IS NULL").
		Order("tunnels.id").Scan(&traffic).Error
	return traffic, err
}

func (r *TunnelRepository) RecordRelayTraffic(id uint, input, output, lastInput, lastOutput, created int64) error {
	return r.DB.Model(&model.GostTunnel{}).Where("id = ?", id).UpdateColumns(map[string]any{
		"relay_input_bytes":        gorm.Expr("relay_input_bytes + ?", input),
		"relay_output_bytes":       gorm.Expr("relay_output_bytes + ?", output),
		"relay_last_input_bytes":   lastInput,
		"relay_last_output_bytes":  lastOutput,
		"relay_service_created_at": created,
	}).Error
}
