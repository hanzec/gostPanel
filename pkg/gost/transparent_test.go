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
