package handler

import (
	"fmt"
	"slices"
	"testing"

	"yagp/internal/model"
	"yagp/internal/repository"
)

func TestNodeHTTPManualAddressesStaySeparateFromReportedIPs(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{
		"name":"mapped", "manual_addresses":[" 203.0.113.8 ","::ffff:203.0.113.8","2001:0db8:0:0::8","2001:db8::8"]
	}`, 200))
	want := []string{"203.0.113.8", "2001:db8::8"}
	if !slices.Equal(node.ManualAddresses, want) || node.Address != "" || node.IPv4 != "" || node.IPv6 != "" || len(node.Addresses) != 0 {
		t.Fatalf("manual addresses were not normalized separately: %+v", node)
	}
	path := fmt.Sprintf("/nodes/%d", node.ID)
	repo := repository.NewNodeRepository(f.db)
	if err := repo.UpdateIPs(t.Context(), &node, "192.0.2.8", "", "192.0.2.8"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":"mapped"}`, `{"name":"mapped","manual_addresses":null}`} {
		updated := handlerData[model.GostNode](t, f.request(t, "PUT", path, body, 200))
		if !slices.Equal(updated.ManualAddresses, want) || updated.Address != "192.0.2.8" || updated.IPv4 != "192.0.2.8" || !slices.Equal(updated.Addresses, []string{"192.0.2.8"}) {
			t.Fatalf("metadata update lost manual addresses or overwrote detected IPs: %+v", updated)
		}
	}
	found := handlerData[handlerPage[model.GostNode]](t, f.request(t, "GET", "/nodes?keyword=203.0.113.8", "", 200))
	if found.Total != 1 || len(found.List) != 1 || found.List[0].ID != node.ID {
		t.Fatalf("manual IP was not searchable: %+v", found)
	}
	for _, raw := range []string{`["192.0.2.1:3333"]`, `["192.0.2.0/24"]`, `["fe80::1%en0"]`, `["not-an-ip"]`, `[""]`, `"203.0.113.8"`, `[7]`, `[null]`} {
		body := `{"name":"mapped","manual_addresses":` + raw + `}`
		f.request(t, "POST", "/nodes", body, 400)
		f.request(t, "PUT", path, body, 400)
	}
	stored := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if !slices.Equal(stored.ManualAddresses, want) {
		t.Fatalf("rejected request modified manual addresses: %+v", stored)
	}
	updated := handlerData[model.GostNode](t, f.request(t, "PUT", path, `{"name":"mapped","manual_addresses":["203.0.113.9"]}`, 200))
	if !slices.Equal(updated.ManualAddresses, []string{"203.0.113.9"}) || updated.Address != "192.0.2.8" {
		t.Fatalf("replacing supplementary IPs changed primary address: %+v", updated)
	}
	f.request(t, "PUT", path, `{"name":"mapped","manual_addresses":[]}`, 200)
	cleared := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if len(cleared.ManualAddresses) != 0 || cleared.Address != "192.0.2.8" || !slices.Equal(cleared.Addresses, []string{"192.0.2.8"}) {
		t.Fatalf("clear did not preserve detected addresses: %+v", cleared)
	}
	found = handlerData[handlerPage[model.GostNode]](t, f.request(t, "GET", "/nodes?keyword=203.0.113.9", "", 200))
	if found.Total != 0 {
		t.Fatalf("cleared IP remains searchable: %+v", found)
	}
}
