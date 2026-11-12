package nodebridge

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

const redirectWebPorts = "{ 80, 443 }"

var redirectBypassPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func redirectNftBatch(table string, port uint32, mark uint32, relayIPs []netip.Addr, dockerAddress netip.Addr, bridges []string, version uint32, revision uint64) []byte {
	var batch strings.Builder
	// Add before delete handles absent tables on kernels without the destroy command.
	fmt.Fprintf(&batch, "add table inet %[1]s\ndelete table inet %[1]s\n", table)
	if port == 0 {
		return []byte(batch.String())
	}
	fmt.Fprintf(&batch, "add table inet %s { comment %q; }\n", table, redirectTableComment(version, revision))
	fmt.Fprintf(&batch, "add set inet %s bypass4 { type ipv4_addr; flags interval; elements = { %s }; }\n",
		table, redirectBypassElements(relayIPs))
	fmt.Fprintf(&batch, "add chain inet %s output { type nat hook output priority -101; policy accept; }\n", table)
	fmt.Fprintf(&batch, "add chain inet %s proxy_output\n", table)
	fmt.Fprintf(&batch, "add rule inet %s output tcp dport %s counter jump proxy_output\n", table, redirectWebPorts)
	appendRedirectBypassRules(&batch, table, "proxy_output", mark)
	fmt.Fprintf(&batch, "add rule inet %s proxy_output meta nfproto ipv4 meta l4proto tcp counter redirect to :%d\n", table, port)
	if dockerAddress.Is4() {
		appendDockerRedirectRules(&batch, table, port, mark, dockerAddress, bridges)
	}
	return []byte(batch.String())
}

func redirectTableComment(version uint32, revision uint64) string {
	return fmt.Sprintf("yagp redirect version %d revision %d", version, revision)
}

func redirectBypassElements(relayIPs []netip.Addr) string {
	elements := make([]string, 0, len(redirectBypassPrefixes)+len(relayIPs))
	for _, prefix := range redirectBypassPrefixes {
		elements = append(elements, prefix.String())
	}
	addresses := make([]netip.Addr, 0, len(relayIPs))
	for _, address := range relayIPs {
		address = address.Unmap()
		if address.Is4() && !slices.ContainsFunc(redirectBypassPrefixes, func(prefix netip.Prefix) bool {
			return prefix.Contains(address)
		}) {
			addresses = append(addresses, address)
		}
	}
	slices.SortFunc(addresses, netip.Addr.Compare)
	for _, address := range slices.Compact(addresses) {
		elements = append(elements, address.String())
	}
	return strings.Join(elements, ", ")
}

func appendRedirectBypassRules(batch *strings.Builder, table, chain string, mark uint32) {
	fmt.Fprintf(batch, "add rule inet %s %s meta mark %#08x counter return\n", table, chain, mark)
	fmt.Fprintf(batch, "add rule inet %s %s meta nfproto ipv6 counter return\n", table, chain)
	fmt.Fprintf(batch, "add rule inet %s %s ip daddr @bypass4 counter return\n", table, chain)
	fmt.Fprintf(batch, "add rule inet %s %s fib daddr type local counter return\n", table, chain)
}

func appendDockerRedirectRules(batch *strings.Builder, table string, port uint32, mark uint32, address netip.Addr, bridges []string) {
	fmt.Fprintf(batch, "add chain inet %s docker_input { type filter hook input priority -101; policy accept; }\n", table)
	if len(bridges) > 0 {
		fmt.Fprintf(batch, "add chain inet %s prerouting { type nat hook prerouting priority -101; policy accept; }\n", table)
		fmt.Fprintf(batch, "add chain inet %s proxy_docker\n", table)
		bridgeNames := make([]string, len(bridges))
		for i, bridge := range bridges {
			bridgeNames[i] = fmt.Sprintf("%q", bridge)
		}
		bridgeSet := "{ " + strings.Join(bridgeNames, ", ") + " }"
		fmt.Fprintf(batch, "add rule inet %s prerouting iifname %s tcp dport %s counter jump proxy_docker\n", table, bridgeSet, redirectWebPorts)
		appendRedirectBypassRules(batch, table, "proxy_docker", mark)
		// DNAT sends all selected bridges to the single Gost listener on a Docker bridge address.
		fmt.Fprintf(batch, "add rule inet %s proxy_docker meta nfproto ipv4 meta l4proto tcp counter dnat ip to %s:%d\n", table, address, port)
		fmt.Fprintf(batch, "add rule inet %s docker_input ip daddr %s tcp dport %d iifname %s ct status dnat counter return\n",
			table, address, port, bridgeSet)
	}
	fmt.Fprintf(batch, "add rule inet %s docker_input ip daddr %s tcp dport %d counter drop\n", table, address, port)
}
