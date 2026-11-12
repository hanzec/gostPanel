package service

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/pkg/gost"
)

func TestManualDomainRulesNormalizeAndBuildMatchers(t *testing.T) {
	svc := NewGostRulesetService(newServiceTestDB(t))
	for _, test := range []struct {
		input, saved, matcher string
	}{
		{" Example.COM. ", "example.com", ".example.com:443"},
		{".Example.COM.", ".example.com", ".example.com:443"},
		{"*.Example.COM", "*.example.com", "*.example.com:443"},
		{"API-*.Example.COM.", "api-*.example.com", "api-*.example.com:443"},
		{"api-?.example.com", "api-?.example.com", "api-?.example.com:443"},
		{"api-??.*.example.com", "api-??.*.example.com", "api-??.*.example.com:443"},
		{"Example.COM.:0443", "example.com:443", "example.com:443"},
		{".Example.COM:443", ".example.com:443", ".example.com:443"},
		{"*.Example.COM:443", "*.example.com:443", "*.example.com:443"},
		{"api-?.example.com:443", "api-?.example.com:443", "api-?.example.com:443"},
		{"example.com:00400-00500", "example.com:400-500", "example.com:400-500"},
		{".example.com:8000-9000", ".example.com:8000-9000", ".example.com:8000-9000"},
		{"*.example.com:400-500", "*.example.com:400-500", "*.example.com:400-500"},
		{"example.com:443-443", "example.com:443-443", "example.com:443-443"},
		{"example.com:0", "example.com:0", "example.com:0"},
		{"example.com:65535", "example.com:65535", "example.com:65535"},
		{".example.com:0-65535", ".example.com:0-65535", ".example.com:0-65535"},
		{"*.example.com:00000-65535", "*.example.com:0-65535", "*.example.com:0-65535"},
		{"xn--fsqu00a.example", "xn--fsqu00a.example", ".xn--fsqu00a.example:443"},
		{"localhost", "localhost", ".localhost:443"},
	} {
		t.Run(test.input, func(t *testing.T) {
			input := []string{test.input}
			_, domains, err := svc.NormalizeSelection(t.Context(), nil, input)
			requireServiceError(t, err, nil)
			if !slices.Equal(domains, []string{test.saved}) || input[0] != test.input {
				t.Fatalf("normalized %q = %v; input = %v", test.input, domains, input)
			}
			matchers, err := svc.SelectedMatchers(t.Context(), nil, domains)
			requireServiceError(t, err, nil)
			if !slices.Equal(matchers, []string{test.matcher}) {
				t.Fatalf("matcher for %q = %v, want %q", test.input, matchers, test.matcher)
			}
		})
	}
	_, domains, err := svc.NormalizeSelection(t.Context(), nil, []string{"Example.COM", ".example.com", "EXAMPLE.COM.", "*.example.com:0443", "*.Example.COM:443"})
	requireServiceError(t, err, nil)
	if !slices.Equal(domains, []string{"example.com", ".example.com", "*.example.com:443"}) {
		t.Fatalf("manual rule deduplication changed saved syntax: %v", domains)
	}
	matchers, err := svc.SelectedMatchers(t.Context(), nil, domains)
	requireServiceError(t, err, nil)
	if !slices.Equal(matchers, []string{".example.com:443", "*.example.com:443"}) {
		t.Fatalf("equivalent bare and explicit suffix rules were not merged: %v", matchers)
	}
}

func TestManualDomainRulesRejectInvalidPortsAndPatterns(t *testing.T) {
	svc := NewGostRulesetService(newServiceTestDB(t))
	for _, domain := range []string{
		"", ".", "..example.com", "example..com", "example.com..", ".*.example.com", ".api-?.example.com",
		"192.0.2.1", ".192.0.2.1:443", "192.0.2.1:443", "2001:db8::1", "[2001:db8::1]:443", "192.0.2.0/24",
		"https://example.com", "example.com/path", "example.com#comment", "example.com,other.com", "example.com;other.com",
		"bad name.example", "bad\nname.example", "bad\x00name.example", "bad_host.example", "-bad.example", "bad-.example", "例子.example",
		"[ab].example.com", "{a,b}.example.com", `\*.example.com`, "*.example.com:abc", "api-?.example.com:",
		"example.com:", "example.com:+443", "example.com:-1", "example.com:65536", "example.com:443:80",
		"example.com:443-", "example.com:-443", "example.com:500-400", "example.com:0-65536", "example.com:80-443-500",
		"example.com: 443", "example.com:400 -500", "example.com:４４３", "example.com:99999999999999999999999999999",
		strings.Repeat("a", 64) + ".example.com", strings.Repeat("a.", 127) + "a",
	} {
		t.Run(domain, func(t *testing.T) {
			ids, domains, err := svc.NormalizeSelection(t.Context(), nil, []string{"valid.example", domain})
			biz, ok := err.(*bizerrors.BizError)
			if !ok || biz.HTTPCode != http.StatusBadRequest || biz.Code != 10421 || ids != nil || domains != nil {
				t.Fatalf("invalid manual rule %q = %v, %v, %v", domain, ids, domains, err)
			}
		})
	}
}

func TestTransparentIngressManualDomainPatternsPersistAndReachBypass(t *testing.T) {
	f := newOriginalTargetTestTunnel(t)
	rules := NewRuleService(f.db)
	input := []string{"Legacy.Example", ".Suffix.Example:443", "*.Wildcard.Example:443", "API-?.Example:400-500", "Exact.Example:443"}
	wantSaved := []string{"legacy.example", ".suffix.example:443", "*.wildcard.example:443", "api-?.example:400-500", "exact.example:443"}
	wantMatchers := []string{".legacy.example:443", ".suffix.example:443", "*.wildcard.example:443", "api-?.example:400-500", "exact.example:443"}
	updated, err := rules.Update(f.destination.ID, &dto.UpdateRuleReq{Name: "manual Gost rules", Protocol: "tcp", ListenPort: f.destination.ListenPort,
		ManualDomains: input}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if !slices.Equal(updated.ManualDomains, wantSaved) {
		t.Fatalf("rule update did not store normalized native patterns: %v", updated.ManualDomains)
	}
	// An unrelated edit must keep the saved patterns, including legacy bare-domain semantics.
	_, err = rules.Update(f.destination.ID, &dto.UpdateRuleReq{Name: "renamed manual rules", Protocol: "tcp", ListenPort: f.destination.ListenPort}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	for range 2 {
		requireServiceError(t, f.start(), nil)
		stored, err := f.service.GetByID(f.tunnel.ID)
		requireServiceError(t, err, nil)
		if !slices.Equal(stored.ExitRule.ManualDomains, wantSaved) {
			t.Fatalf("reloaded exit rule changed manual patterns: %v", stored.ExitRule.ManualDomains)
		}
		var bypass gost.AdmissionConfig
		f.entryNode.read("/config/bypasses/"+transparentPolicyName(f.tunnel.ID), &bypass)
		if !bypass.Whitelist || !slices.Equal(bypass.Matchers, wantMatchers) {
			t.Fatalf("manual rules reached Gost with changed matching semantics: %+v", bypass)
		}
		requireServiceError(t, f.stop(), nil)
		f.service = NewTunnelService(f.db)
	}
}
