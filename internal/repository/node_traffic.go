package repository

import "context"

// RelayUsageByNode includes both shared exit counters and older per-tunnel counters.
// Soft-deleted endpoints and tunnels still contribute to a node's lifetime usage.
func (r *NodeRepository) RelayUsageByNode(ctx context.Context, ids []uint) (map[uint]uint64, error) {
	var totals []struct {
		NodeID      uint
		InputBytes  uint64
		OutputBytes uint64
	}
	err := r.DB.WithContext(ctx).Raw(`
		SELECT node_id, SUM(relay_input_bytes) AS input_bytes, SUM(relay_output_bytes) AS output_bytes
		FROM (
			SELECT node_id, relay_input_bytes, relay_output_bytes FROM rules
			WHERE node_id IN ? AND relay_service_created_at > 0
			UNION ALL
			SELECT exit_node_id, relay_input_bytes, relay_output_bytes FROM tunnels
			WHERE exit_node_id IN ? AND relay_service_created_at > 0
		) AS relay_samples
		GROUP BY node_id`, ids, ids).Scan(&totals).Error
	if err != nil {
		return nil, err
	}
	usage := make(map[uint]uint64, len(totals))
	for _, total := range totals {
		usage[total.NodeID] = total.InputBytes + total.OutputBytes
	}
	return usage, nil
}
