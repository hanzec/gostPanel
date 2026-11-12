package handler

import (
	"fmt"
	"slices"
	"testing"

	"yagp/internal/model"
)

func TestNodeHTTPLegacyManualAddressesAreReadOnly(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{
		"name":"mapped","manual_addresses":["203.0.113.8"]
	}`, 200))
	if len(node.ManualAddresses) != 0 {
		t.Fatalf("create saved obsolete supplemental addresses: %+v", node)
	}
	want := []string{"203.0.113.8"}
	node.ManualAddresses = want
	if err := f.db.Model(&node).Select("ManualAddresses").Updates(&node).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/nodes/%d", node.ID)
	for _, raw := range []string{`["203.0.113.9"]`, `[]`} {
		f.request(t, "PUT", path, `{"name":"renamed","manual_addresses":`+raw+`}`, 200)
		updated := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
		if updated.Name != "renamed" || !slices.Equal(updated.ManualAddresses, want) {
			t.Fatalf("metadata update changed legacy addresses: %+v", updated)
		}
	}
	found := handlerData[handlerPage[model.GostNode]](t, f.request(t, "GET", "/nodes?keyword=203.0.113.8", "", 200))
	if found.Total != 0 {
		t.Fatalf("obsolete supplemental IP remains searchable: %+v", found)
	}
}
