package nodebridge

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func TestRedirectNftBatchFollowsIngressTemplate(t *testing.T) {
	relay := []netip.Addr{
		netip.MustParseAddr("203.0.113.4"), netip.MustParseAddr("203.0.113.4"),
		netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("2001:db8::1"),
	}
	batch := string(redirectNftBatch("yagp_redirect", 12345, 100, relay, netip.Addr{}, nil, 2, 7))
	for _, part := range []string{
		"add table inet yagp_redirect\ndelete table inet yagp_redirect\nadd table inet yagp_redirect { comment \"yagp redirect version 2 revision 7\"; }",
		"100.64.0.0/10", "169.254.0.0/16", "224.0.0.0/4", "203.0.113.4",
		"hook output priority -101", "tcp dport { 80, 443 } counter jump proxy_output",
		"proxy_output meta mark 0x00000064 counter return", "meta nfproto ipv6 counter return",
		"ip daddr @bypass4 counter return", "fib daddr type local counter return",
		"proxy_output meta nfproto ipv4 meta l4proto tcp counter redirect to :12345",
	} {
		if !strings.Contains(batch, part) {
			t.Errorf("redirect batch is missing %q:\n%s", part, batch)
		}
	}
	if strings.Count(batch, "203.0.113.4") != 1 || strings.Contains(batch, "2001:db8::1") ||
		strings.Contains(batch, "10.0.0.1") || strings.Contains(batch, "prerouting") {
		t.Fatalf("unexpected address or Docker rule in host-only batch:\n%s", batch)
	}
	cleanup := string(redirectNftBatch("yagp_redirect", 0, 0, nil, netip.Addr{}, nil, 2, 8))
	if cleanup != "add table inet yagp_redirect\ndelete table inet yagp_redirect\n" {
		t.Fatalf("cleanup must affect only the managed table: %q", cleanup)
	}
}

func TestRedirectNftBatchRoutesDockerToBoundListener(t *testing.T) {
	address := netip.MustParseAddr("172.17.0.1")
	bridges := []string{"docker0", "br-4d7902c53a23"}
	batch := string(redirectNftBatch("yagp_redirect", 12345, 0x59414750, nil, address, bridges, 1, 1))
	for _, part := range []string{
		"hook prerouting priority -101", `iifname { "docker0", "br-4d7902c53a23" } tcp dport { 80, 443 } counter jump proxy_docker`,
		"proxy_docker meta mark 0x59414750 counter return", "proxy_docker ip daddr @bypass4 counter return",
		"proxy_docker fib daddr type local counter return", "proxy_docker meta nfproto ipv4 meta l4proto tcp counter dnat ip to 172.17.0.1:12345",
		`docker_input ip daddr 172.17.0.1 tcp dport 12345 iifname { "docker0", "br-4d7902c53a23" } ct status dnat counter return`,
		"docker_input ip daddr 172.17.0.1 tcp dport 12345 counter drop",
	} {
		if !strings.Contains(batch, part) {
			t.Errorf("Docker batch is missing %q:\n%s", part, batch)
		}
	}
	withoutBridge := string(redirectNftBatch("yagp_redirect", 12345, 100, nil, address, nil, 1, 2))
	if strings.Contains(withoutBridge, "prerouting") || !strings.Contains(withoutBridge, "docker_input ip daddr 172.17.0.1 tcp dport 12345 counter drop") {
		t.Fatalf("saved Docker listener must stay guarded when its bridge is absent:\n%s", withoutBridge)
	}
}

func TestRedirectMarkAvoidsForeignMarkRules(t *testing.T) {
	const preferred = uint32(0x59414750)
	const own = `{"rule":{"family":"inet","table":"yagp_redirect","chain":"proxy_output","expr":[{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":1497450320}}]}}`
	if mark, err := selectRedirectMark([]byte(`{"nftables":[`+own+`]}`), "yagp_redirect", preferred); err != nil || mark != preferred {
		t.Fatalf("existing managed rule changed selected mark to %#x: %v", mark, err)
	}
	tests := []struct {
		name     string
		rule     string
		reserved func(uint32) bool
	}{
		{"literal mark", `{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":1497450320}}`, func(mark uint32) bool { return mark == preferred }},
		{"high bits", `{"match":{"op":"==","left":{"&":[{"meta":{"key":"mark"}},4278190080]},"right":1493172224}}`, func(mark uint32) bool { return mark&0xff000000 == preferred&0xff000000 }},
		{"nonzero bits", `{"match":{"op":"!=","left":{"&":[{"meta":{"key":"mark"}},65536]},"right":0}}`, func(mark uint32) bool { return mark&0x00010000 != 0 }},
		{"masked assignment", `{"mangle":{"key":{"ct":{"key":"mark"}},"value":{"|":[{"&":[{"ct":{"key":"mark"}},4294901760]},18256]}}}`, func(mark uint32) bool { return mark&0xffff == preferred&0xffff }},
		{"anonymous interval", `{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":{"set":[{"range":[1497450300,1497450340]},2]}}}`, func(mark uint32) bool { return mark >= 1497450300 && mark <= 1497450340 || mark == 2 }},
		{"named set", `{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":"@assigned"}}`, func(mark uint32) bool { return mark == preferred }},
		{"less than", `{"match":{"op":"<","left":{"meta":{"key":"mark"}},"right":"0x59414751"}}`, func(mark uint32) bool { return mark < preferred+1 }},
		{"at most", `{"match":{"op":"<=","left":{"meta":{"key":"mark"}},"right":"0x59414750"}}`, func(mark uint32) bool { return mark <= preferred }},
		{"greater than", `{"match":{"op":">","left":{"meta":{"key":"mark"}},"right":"0x5941474f"}}`, func(mark uint32) bool { return mark > preferred-1 }},
		{"at least", `{"match":{"op":">=","left":{"meta":{"key":"mark"}},"right":"0x59414750"}}`, func(mark uint32) bool { return mark >= preferred }},
		{"exclusive OR", `{"match":{"op":"==","left":{"^":[{"meta":{"key":"mark"}},255]},"right":"0x594147af"}}`, func(mark uint32) bool { return mark == preferred }},
		{"left shift", `{"match":{"op":"==","left":{"<<":[{"meta":{"key":"mark"}},8]},"right":"0x41475000"}}`, func(mark uint32) bool { return mark&0xffffff == preferred&0xffffff }},
		{"right shift", `{"match":{"op":"==","left":{">>":[{"meta":{"key":"mark"}},24]},"right":89}}`, func(mark uint32) bool { return mark>>24 == preferred>>24 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.reserved(preferred) {
				t.Fatal("test must reserve the preferred mark")
			}
			ruleset := fmt.Sprintf(`{"nftables":[{"set":{"family":"ip","table":"existing","name":"assigned","elem":[{"elem":{"val":1497450320,"comment":"assigned mark"}}]}},{"rule":{"family":"ip","table":"existing","chain":"output","expr":[%s]}}]}`, test.rule)
			mark, err := selectRedirectMark([]byte(ruleset), "yagp_redirect", preferred)
			if err != nil || mark == 0 || test.reserved(mark) {
				t.Fatalf("selected reserved mark %#x: %v", mark, err)
			}
		})
	}
}

func TestRedirectMarkReportsUnavailableOrUninspectableRules(t *testing.T) {
	for _, ruleset := range []string{
		`{}`,
		`{"nftables":[{"rule":{"family":"inet","table":"all","expr":[{"match":{"op":"!=","left":{"meta":{"key":"mark"}},"right":0}}]}}]}`,
		`{"nftables":[{"rule":{"family":"inet","table":"all","expr":[{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":"@missing"}}]}}]}`,
	} {
		if mark, err := selectRedirectMark([]byte(ruleset), "yagp_redirect", 100); err == nil || mark != 0 {
			t.Fatalf("unavailable mark returned %#x, %v", mark, err)
		}
	}
}

func TestRedirectMarkRetainsPreferredValueAcrossMarkCopies(t *testing.T) {
	ruleset := []byte(`{"nftables":[{"rule":{"family":"inet","table":"firewall","chain":"output","expr":[
		{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":100}},
		{"mangle":{"key":{"ct":{"key":"mark"}},"value":{"meta":{"key":"mark"}}}}
	]}}]}`)
	if mark, err := selectRedirectMark(ruleset, "yagp_redirect", 100); err != nil || mark != 100 {
		t.Fatalf("unrelated port or mark copy changed the preferred mark to %#x, %v", mark, err)
	}
}

func TestRedirectMarkRejectsUnsupportedRuleExpressions(t *testing.T) {
	tests := []struct {
		expr, diagnostic string
	}{
		{`{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":{"ct":{"key":"mark"}}}}`, "unsupported mark comparison"},
		{`{"match":{"op":"==","left":{"&":[{"meta":{"key":"mark"}}]},"right":100}}`, "unsupported mark comparison"},
		{`{"match":{"op":"==","left":{"&":[{"meta":{"key":"mark"}},"0x100000000"]},"right":100}}`, "unsupported mark comparison"},
		{`{"match":{"op":"==","left":{"&":[{"meta":{"key":"mark"}},{"payload":{"protocol":"tcp","field":"dport"}}]},"right":100}}`, "unsupported mark comparison"},
		{`{"match":{"op":"==","left":{"+":[{"meta":{"key":"mark"}},1]},"right":100}}`, "unsupported mark comparison"},
		{`{"match":{"op":">=","left":{"meta":{"key":"mark"}},"right":1.5}}`, "unsupported ordered mark comparison"},
		{`{"match":{"op":"unknown","left":{"meta":{"key":"mark"}},"right":100}}`, "unsupported mark comparison operator"},
		{`{"mangle":{"key":{"meta":{"key":"mark"}},"value":{"payload":{"protocol":"tcp","field":"dport"}}}}`, "unsupported mark assignment"},
		{`{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":"symbolic"}}`, "unsupported mark match value"},
		{`{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":{"invalid":100}}}`, "unsupported mark match value"},
		{`{"match":{"op":"==","left":{"meta":{"key":"mark"}},"right":{"set":[100,false]}}}`, "unsupported mark match value"},
	}
	for _, test := range tests {
		ruleset := fmt.Sprintf(`{"nftables":[{"rule":{"family":"ip","table":"existing","chain":"output","expr":[%s]}}]}`, test.expr)
		mark, err := selectRedirectMark([]byte(ruleset), "yagp_redirect", 100)
		if err == nil || mark != 0 || !strings.Contains(err.Error(), "ip existing output: "+test.diagnostic) {
			t.Errorf("mark with %s = %#x, %v, want %q", test.expr, mark, err, test.diagnostic)
		}
	}
	if mark, err := selectRedirectMark([]byte("{"), "yagp_redirect", 100); mark != 0 || err == nil ||
		!strings.Contains(err.Error(), "read nftables marks") {
		t.Fatalf("malformed ruleset selected %#x, %v", mark, err)
	}
}
