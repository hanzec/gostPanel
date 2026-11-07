package nodegrpc

import "strconv"

const MaxNftConfigBytes = 256 << 10

func NftTableName(id uint64) string {
	return "yagp_node_" + strconv.FormatUint(id, 10)
}
