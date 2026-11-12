package nodebridge

import (
	"bytes"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
)

func TestRedirectNftBatchInIsolatedNetworkNamespace(t *testing.T) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		t.Skip("nftables is unavailable")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is unavailable")
	}
	namespace := []string{"--user", "--map-root-user", "--net", "--"}
	if output, err := exec.CommandContext(t.Context(), unshare, append(namespace, "true")...).CombinedOutput(); err != nil {
		namespace = []string{"--net", "--"}
		if output, err = exec.CommandContext(t.Context(), unshare, append(namespace, "true")...).CombinedOutput(); err != nil {
			t.Skipf("isolated network namespaces are unavailable: %v (%s)", err, output)
		}
	}
	const table = "yagp_test_redirect"
	relay := []netip.Addr{netip.MustParseAddr("203.0.113.4")}
	hostOnly := redirectNftBatch(table, 12345, 100, relay, netip.Addr{}, nil, 1, 1)
	withDocker := redirectNftBatch(table, 12345, 101, relay, netip.MustParseAddr("172.17.0.1"), []string{"docker0", "br-4d7902c53a23"}, 1, 2)
	cleanup := redirectNftBatch(table, 0, 0, nil, netip.Addr{}, nil, 1, 3)
	script := fmt.Sprintf(`set -eu
"$1" -f - <<'RULES'
add table inet yagp_test_foreign
add chain inet yagp_test_foreign check_mark
add rule inet yagp_test_foreign check_mark meta mark & 0xff000000 == 0x59000000 return
RULES
"$1" -f - <<'RULES'
%sRULES
"$1" -f - <<'RULES'
%sRULES
"$1" -j -nn list ruleset
"$1" -f - <<'RULES'
%sRULES
if "$1" list table inet %s >/dev/null 2>&1; then exit 1; fi
"$1" list table inet yagp_test_foreign >/dev/null
`, hostOnly, withDocker, cleanup, table)
	command := exec.CommandContext(t.Context(), unshare, append(namespace, "sh", "-s", "--", nft)...)
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("apply and remove rules in isolated network namespace: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte(redirectTableComment(1, 2))) ||
		!bytes.Contains(output, []byte(`"dnat"`)) || !bytes.Contains(output, []byte("172.17.0.1")) {
		t.Fatalf("isolated nftables table did not contain the expected versioned Docker NAT rules:\n%s", output)
	}
	const preferred = 0x59414750
	if selected, err := selectRedirectMark(output, table, preferred); err != nil || selected&0xff000000 == 0x59000000 {
		t.Fatalf("selected mark %#x conflicts with live masked mark, error %v:\n%s", selected, err, output)
	}
}
