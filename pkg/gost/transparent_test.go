package gost

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestEnsureMarkedClashChainRefreshesAnExistingRoute(t *testing.T) {
	client, calls := controlFixture(t)
	const path = "/config/chains/transparent"
	const source = `{"name":"fixed","hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":"192.0.2.10:443"}]}]}`
	const current = `{"name":"transparent","hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":"192.0.2.4:443"}]}]}`
	const updated = `{"name":"transparent","hops":[{"name":"hop-0","bypass":"yagp-transparent-proxy","sockopts":{"mark":100},"nodes":[{"name":"exit-relay","addr":"192.0.2.10:443","sockopts":{"mark":100}}]}]}`
	for _, tc := range []struct {
		name      string
		rpcErr    error
		wantError string
	}{
		{name: "updated"},
		{name: "node disconnected", rpcErr: status.Error(codes.Unavailable, "node disconnected"), wantError: "node disconnected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls <- gostCommand(http.MethodGet, path, "", http.StatusOK, `{"data":`+current+`}`)
			calls <- gostCommand(http.MethodGet, "/config/chains/fixed", "", http.StatusOK, `{"data":`+source+`}`)
			update := gostCommand(http.MethodPut, path, updated, http.StatusOK, `{}`)
			update.err = tc.rpcErr
			calls <- update
			err := client.EnsureMarkedClashChain("fixed", "transparent", 100)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("ensure marked chain = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestBuildClashRedirectServiceUsesSelectedMark(t *testing.T) {
	for _, tc := range []struct {
		service *ServiceConfig
		name    string
		mark    int
	}{
		{BuildClashRedirectService("rule-7"), "rule-7", ClashRedirectMark},
		{BuildMarkedClashRedirectService("rule-7-docker", 100), "rule-7-docker", 100},
	} {
		service := tc.service
		if service.Name != tc.name || service.Handler.Type != "red" || service.Listener.Type != "red" ||
			service.SockOpts.Mark != tc.mark || service.Handler.Metadata["sniffing"] != true {
			t.Fatalf("unexpected RED service for mark %d: %+v", tc.mark, service)
		}
	}
}

func TestTransparentRouteUsesItsSelectedWhitelistOrOriginalDestination(t *testing.T) {
	const path = "/config/chains/transparent"
	const chain = `{"name":"fixed","hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":"192.0.2.4:443"}]}]}`
	for _, bypass := range []string{"tunnel-7-bypass", ""} {
		client, calls := controlFixture(t)
		filter := ""
		if bypass != "" {
			filter = fmt.Sprintf(`"bypass":%q,`, bypass)
		}
		want := fmt.Sprintf(`{"name":"transparent","hops":[{"name":"hop-0",%s"sockopts":{"mark":100},"nodes":[{"name":"exit-relay","addr":"192.0.2.4:443","sockopts":{"mark":100}}]}]}`, filter)
		calls <- gostCommand("GET", path, "", 200, `{"data":`+chain+`}`)
		calls <- gostCommand("GET", "/config/chains/fixed", "", 200, `{"data":`+chain+`}`)
		calls <- gostCommand("PUT", path, want, 200, "{}")
		if err := client.EnsureMarkedTransparentChain("fixed", "transparent", 100, bypass); err != nil {
			t.Fatal(err)
		}
		svc := BuildMarkedTransparentRedirectService("rule-7", 100, bypass != "")
		if svc.Listener.Type != "red" || svc.Handler.Type != "red" || svc.SockOpts.Mark != 100 ||
			(svc.Handler.Metadata["sniffing"] == true) != (bypass != "") {
			t.Fatalf("incorrect RED settings for bypass %q: %+v", bypass, svc)
		}
	}
}

func TestNamedTransparentBypassUsesItsOwnResource(t *testing.T) {
	client, calls := controlFixture(t)
	const name = "tunnel-7-bypass"
	const path = "/config/bypasses/" + name
	const config = `{"name":"tunnel-7-bypass","whitelist":true,"matchers":[".example.test"]}`
	calls <- gostCommand("GET", path, "", 200, `{"data":null}`)
	calls <- gostCommand("POST", "/config/bypasses", config, 201, "{}")
	if changed, err := client.SetNamedTransparentBypass(name, []string{".example.test"}); err != nil || !changed {
		t.Fatalf("named bypass creation: changed=%t err=%v", changed, err)
	}
	calls <- gostCommand("GET", path, "", 200, `{"data":`+config+`}`)
	calls <- gostCommand("DELETE", path, "", 204, "")
	calls <- gostCommand("GET", path, "", 200, `{"data":null}`)
	for range 2 {
		if err := client.DeleteNamedTransparentBypass(name); err != nil {
			t.Fatal(err)
		}
	}
}
