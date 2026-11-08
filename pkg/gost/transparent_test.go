package gost

import (
	"fmt"
	"testing"
)

func TestEnsureClashChainKeepsFixedRouteAndMarksRelay(t *testing.T) {
	client, calls := controlFixture(t)
	const sourcePath = "/config/chains/fixed"
	const clashPath = "/config/chains/transparent"
	const source = `{"name":"fixed","metadata":{"retry":2},"hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":"192.0.2.4:443","connector":{"type":"relay","custom":true}}]}]}`
	converted := fmt.Sprintf(`{"name":"transparent","metadata":{"retry":2},"hops":[{"name":"hop-0","bypass":"%s","sockopts":{"mark":%d},"nodes":[{"name":"exit-relay","addr":"192.0.2.4:443","connector":{"type":"relay","custom":true},"sockopts":{"mark":%d}}]}]}`,
		TransparentBypassName, ClashRedirectMark, ClashRedirectMark)
	missing := gostCommand("GET", clashPath, "", 200, `{"data":null}`)
	lookup := gostCommand("GET", sourcePath, "", 200, `{"data":`+source+`}`)
	for _, scenario := range []struct {
		name  string
		fail  bool
		calls []controlCall
	}{
		{"clone", false, []controlCall{missing, lookup, gostCommand("POST", "/config/chains", converted, 201, `{}`)}},
		{"already created", false, []controlCall{gostCommand("GET", clashPath, "", 200, `{"data":`+converted+`}`)}},
		{"lookup unavailable", true, []controlCall{gostCommand("GET", clashPath, "", 503, "")}},
		{"source unavailable", true, []controlCall{missing, gostCommand("GET", sourcePath, "", 503, "")}},
		{"source missing", true, []controlCall{missing, gostCommand("GET", sourcePath, "", 200, `{"data":null}`)}},
		{"source without managed first hop", true, []controlCall{missing, gostCommand("GET", sourcePath, "", 200, `{"data":{"hops":[{"name":"different"}]}}`)}},
		{"source without relay", true, []controlCall{missing, gostCommand("GET", sourcePath, "", 200, `{"data":{"hops":[{"name":"hop-0","nodes":[]}]}}`)}},
		{"rejected clone", true, []controlCall{missing, lookup, gostCommand("POST", "/config/chains", converted, 409, `{}`)}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			for _, call := range scenario.calls {
				calls <- call
			}
			if err := client.EnsureClashChain("fixed", "transparent"); (err != nil) != scenario.fail {
				t.Fatalf("ensure Clash chain: %v, want error %t", err, scenario.fail)
			}
		})
	}
}
