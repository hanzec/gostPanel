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
	TypedExit      bool               `json:"-"`
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
			COALESCE(rules.role = 'exit', FALSE) AS typed_exit,
			nodes.name AS exit_node_name, nodes.status AS exit_node_status,
			tunnels.relay_input_bytes AS input_bytes, tunnels.relay_output_bytes AS output_bytes,
			(tunnels.relay_service_created_at > 0) AS measured`).
		Joins("JOIN nodes ON nodes.id = tunnels.exit_node_id AND nodes.deleted_at IS NULL").
		Joins("LEFT JOIN rules ON rules.id = tunnels.exit_rule_id AND rules.deleted_at IS NULL").
		Order("tunnels.id").Scan(&traffic).Error
	return traffic, err
}

type EndpointExitTraffic struct {
	ID             uint             `json:"id"`
	Name           string           `json:"name"`
	ExitNodeID     uint             `json:"exit_node_id"`
	ExitNodeName   string           `json:"exit_node_name"`
	ExitNodeStatus model.NodeStatus `json:"exit_node_status"`
	TrafficTotals
	Running  bool `json:"running"`
	Measured bool `json:"measured"`
}

func (r *TunnelRepository) EndpointExitTraffic() ([]EndpointExitTraffic, error) {
	traffic := make([]EndpointExitTraffic, 0)
	// Existing tunnel counters remain historical, including after the tunnel is removed.
	history := r.DB.Table("tunnels").
		Select(`exit_rule_id, SUM(relay_input_bytes) AS input_bytes, SUM(relay_output_bytes) AS output_bytes,
			MAX(relay_service_created_at) AS measured_at,
			MAX(CASE WHEN deleted_at IS NULL AND status = ? THEN 1 ELSE 0 END) AS running`, model.TunnelStatusRunning).
		Where("exit_rule_id IS NOT NULL").Group("exit_rule_id")
	err := r.DB.Model(&model.GostRule{}).
		Select(`rules.id, rules.name, rules.node_id AS exit_node_id, nodes.name AS exit_node_name, nodes.status AS exit_node_status,
			rules.relay_input_bytes + COALESCE(history.input_bytes, 0) AS input_bytes,
			rules.relay_output_bytes + COALESCE(history.output_bytes, 0) AS output_bytes,
			COALESCE(history.running, 0) AS running,
			(rules.relay_service_created_at > 0 OR COALESCE(history.measured_at, 0) > 0) AS measured`).
		Joins("JOIN nodes ON nodes.id = rules.node_id AND nodes.deleted_at IS NULL").
		Joins("LEFT JOIN (?) AS history ON history.exit_rule_id = rules.id", history).
		Where("rules.role = ?", model.EndpointRoleExit).
		Order("rules.id").Scan(&traffic).Error
	return traffic, err
}

func (r *TunnelRepository) RecordRelayTraffic(id uint, input, output, lastInput, lastOutput, created int64) error {
	return r.DB.Model(&model.GostTunnel{}).Where("id = ?", id).UpdateColumns(relayTrafficUpdate(input, output, lastInput, lastOutput, created)).Error
}

func (r *RuleRepository) RecordExitRelayTraffic(id uint, serviceID string, input, output, lastInput, lastOutput, created int64) error {
	values := relayTrafficUpdate(input, output, lastInput, lastOutput, created)
	values["relay_counter_id"] = serviceID
	return r.DB.Model(&model.GostRule{}).Where("id = ?", id).UpdateColumns(values).Error
}

func relayTrafficUpdate(input, output, lastInput, lastOutput, created int64) map[string]any {
	return map[string]any{
		"relay_input_bytes":        gorm.Expr("relay_input_bytes + ?", input),
		"relay_output_bytes":       gorm.Expr("relay_output_bytes + ?", output),
		"relay_last_input_bytes":   lastInput,
		"relay_last_output_bytes":  lastOutput,
		"relay_service_created_at": created,
	}
}

// HistoricalRelayTraffic reads the old per-tunnel checkpoint when an existing Relay starts reporting to its exit endpoint.
func (r *TunnelRepository) HistoricalRelayTraffic(tunnelID, ruleID, nodeID uint) (model.RelayTraffic, error) {
	var tunnel model.GostTunnel
	err := r.DB.Unscoped().Where("id = ? AND exit_rule_id = ? AND exit_node_id = ?", tunnelID, ruleID, nodeID).
		Select("relay_last_input_bytes", "relay_last_output_bytes", "relay_service_created_at").
		Find(&tunnel).Error
	return tunnel.RelayTraffic, err
}
