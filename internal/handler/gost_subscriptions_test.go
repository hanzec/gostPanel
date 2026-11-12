package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yagp/internal/model"
)

func TestGostSubscriptionHTTPCRUDAndReferences(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "DOMAIN-SUFFIX,subscription.example\nIP-CIDR,192.0.2.0/24")
	}))
	defer upstream.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	const collection = "/system/gost-rulesets"
	if list := handlerData[[]gostRulesetSummary](t, f.request(t, "GET", collection, "", 200)); list == nil || len(list) != 0 {
		t.Fatalf("empty inventory = %v", list)
	}
	for _, body := range []string{"", "null", `{}`, `{"name":"Missing URL"}`, `{"url":"https://example.test/rules"}`,
		`{"name":"Invalid","url":"http://example.test/rules"}`} {
		f.request(t, "POST", collection, body, 400)
	}
	body, _ := json.Marshal(map[string]string{"name": "Named subscription", "url": upstream.URL})
	created := handlerData[gostRulesetSummary](t, f.request(t, "POST", collection, string(body), 200))
	if created.ID != 2 || created.Name != "Named subscription" || created.MatcherCount != 2 || created.UpdatedAt == nil {
		t.Fatalf("created subscription summary = %+v", created)
	}
	list := handlerData[[]map[string]any](t, f.request(t, "GET", collection, "", 200))
	if len(list) != 1 || list[0]["id"] != float64(created.ID) || list[0]["matcher_count"] != float64(2) ||
		list[0]["matchers"] != nil || list[0]["node_ids"] != nil {
		t.Fatalf("subscription inventory leaked internals or lost summary fields: %v", list)
	}
	f.request(t, "PUT", collection+"/0", string(body), 400)
	f.request(t, "PUT", collection+"/invalid", string(body), 400)
	f.request(t, "PUT", collection+"/99", string(body), 404)
	path := fmt.Sprintf("%s/%d", collection, created.ID)
	body, _ = json.Marshal(map[string]string{"name": "Renamed subscription", "url": upstream.URL})
	updated := handlerData[gostRulesetSummary](t, f.request(t, "PUT", path, string(body), 200))
	if updated.ID != created.ID || updated.Name != "Renamed subscription" {
		t.Fatalf("updated subscription summary = %+v", updated)
	}
	rule := model.GostRule{Name: "Selected endpoint", RulesetIDs: []uint{created.ID}}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	blocked := f.request(t, "DELETE", path, "", 400)
	if !strings.Contains(blocked.Message, "移除引用") {
		t.Fatalf("referenced delete returned no actionable explanation: %+v", blocked)
	}
	if err := f.db.Delete(&rule).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, "DELETE", path, "", 200)
	f.request(t, "DELETE", path, "", 404)
	f.request(t, "DELETE", collection+"/0", "", 400)
	if reply := f.request(t, "DELETE", collection+"/1", "", 400); !strings.Contains(reply.Message, "默认规则集不能删除") {
		t.Fatalf("catalog exposed legacy default deletion: %+v", reply)
	}
	if list := handlerData[[]gostRulesetSummary](t, f.request(t, "GET", collection, "", 200)); len(list) != 0 {
		t.Fatalf("deleted subscription remained in inventory: %+v", list)
	}
}

func TestEndpointRulesetValidationReturnsBadRequest(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	node := model.GostNode{Name: "selection validation"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{`"ruleset_ids":[999]`, `"manual_domains":["https://invalid.example"]`} {
		body := fmt.Sprintf(`{"name":"invalid selection","node_id":%d,"role":"exit","protocol":"tcp","listen_port":18001,"use_original_target":true,%s}`, node.ID, selection)
		reply := f.request(t, "POST", "/rules", body, http.StatusBadRequest)
		if reply.Code != 10421 || (!strings.Contains(reply.Message, "999") && !strings.Contains(reply.Message, "https://invalid.example")) {
			t.Fatalf("selection error lost its validation code or detail: %+v", reply)
		}
	}
}
