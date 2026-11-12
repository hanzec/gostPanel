package nodegrpc

import "strconv"

const MaxNftConfigBytes = 256 << 10
const SingleNodeNftTable = "yagp_node"

func NftTableName(id uint64) string {
	return "yagp_node_" + strconv.FormatUint(id, 10)
}

func TransparentRedirectTableName(id uint64) string {
	return "yagp_redirect_" + strconv.FormatUint(id, 10)
}
