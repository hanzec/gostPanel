package handler

import (
	"net/http"
	"slices"
	"testing"

	"yagp/internal/model"
)

func TestGostRulesetPUTRequiresExplicitFields(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	const endpoint = "/system/gost-ruleset"
	initial := model.GostRuleset{
		ID:       1,
		URL:      "https://example.org/rules.yaml",
		NodeIDs:  []uint{42},
		Matchers: []string{"example.org"},
	}
	if err := f.db.Create(&initial).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty request", ""},
		{"empty object", `{}`},
		{"null body", `null`},
		{"missing URL", `{"node_ids":[]}`},
		{"missing node IDs", `{"url":""}`},
		{"null URL", `{"url":null,"node_ids":[]}`},
		{"null node IDs", `{"url":"","node_ids":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.request(t, http.MethodPut, endpoint, tc.body, http.StatusBadRequest)
			got := handlerData[model.GostRuleset](t, f.request(t, http.MethodGet, endpoint, "", http.StatusOK))
			if got.URL != initial.URL || !slices.Equal(got.NodeIDs, initial.NodeIDs) || !slices.Equal(got.Matchers, initial.Matchers) {
				t.Fatalf("invalid request changed the ruleset: %+v", got)
			}
		})
	}
	f.request(t, http.MethodPut, endpoint, `{"url":"","node_ids":[]}`, http.StatusOK)
	got := handlerData[model.GostRuleset](t, f.request(t, http.MethodGet, endpoint, "", http.StatusOK))
	if got.URL != "" || len(got.NodeIDs) != 0 || len(got.Matchers) != 0 {
		t.Fatalf("explicit empty values did not clear the ruleset: %+v", got)
	}
}
