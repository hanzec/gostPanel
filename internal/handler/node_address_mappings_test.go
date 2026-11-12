package handler

import (
	"fmt"
	"slices"
	"testing"

	"yagp/internal/model"
	"yagp/internal/repository"
)

func TestNodeHTTPAddressMappings(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{
		"name":"mapped",
		"manual_addresses":["203.0.113.8"],
		"address_mappings":[
			{"listen_ip":" ::ffff:10.0.0.8 ", "connect_ip":" 198.51.100.8 "},
			{"listen_ip":"2001:0db8:0:0::8", "connect_ip":"2001:db8:1::8"}
		]
	}`, 200))
	want := []model.NodeAddressMapping{
		{ListenIP: "10.0.0.8", ConnectIP: "198.51.100.8"},
		{ListenIP: "2001:db8::8", ConnectIP: "2001:db8:1::8"},
	}
	if !slices.Equal(node.AddressMappings, want) {
		t.Fatalf("created mappings = %v, want %v", node.AddressMappings, want)
	}
	path := fmt.Sprintf("/nodes/%d", node.ID)
	repo := repository.NewNodeRepository(f.db)
	if err := repo.UpdateIPs(t.Context(), &node, "192.0.2.8", "", "192.0.2.8"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":"mapped"}`, `{"name":"mapped","address_mappings":null}`} {
		updated := handlerData[model.GostNode](t, f.request(t, "PUT", path, body, 200))
		if !slices.Equal(updated.AddressMappings, want) || updated.Address != "192.0.2.8" {
			t.Fatalf("metadata update lost mappings or changed the detected address: %+v", updated)
		}
	}
	for _, ip := range []string{want[0].ListenIP, want[0].ConnectIP} {
		found := handlerData[handlerPage[model.GostNode]](t, f.request(t, "GET", "/nodes?keyword="+ip, "", 200))
		if found.Total != 1 || len(found.List) != 1 || found.List[0].ID != node.ID || !slices.Equal(found.List[0].AddressMappings, want) {
			t.Fatalf("mapped IP %s was not searchable: %+v", ip, found)
		}
	}
	replaced := handlerData[model.GostNode](t, f.request(t, "PUT", path, `{
		"name":"mapped","address_mappings":[{"listen_ip":"10.0.0.8","connect_ip":"198.51.100.9"}]
	}`, 200))
	if !slices.Equal(replaced.AddressMappings, []model.NodeAddressMapping{{ListenIP: "10.0.0.8", ConnectIP: "198.51.100.9"}}) {
		t.Fatalf("mappings were not replaced: %+v", replaced)
	}
	f.request(t, "PUT", path, `{"name":"mapped","address_mappings":[]}`, 200)
	cleared := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if len(cleared.AddressMappings) != 0 || cleared.Address != "192.0.2.8" ||
		!slices.Equal(cleared.ManualAddresses, []string{"203.0.113.8"}) {
		t.Fatalf("clearing mappings changed other node addresses: %+v", cleared)
	}
}

func TestNodeHTTPRejectsInvalidAddressMappings(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{
		"name":"mapped","address_mappings":[{"listen_ip":"10.0.0.8","connect_ip":"198.51.100.8"}]
	}`, 200))
	path := fmt.Sprintf("/nodes/%d", node.ID)
	for _, test := range []struct {
		name string
		raw  string
	}{
		{"non-array", `{"listen_ip":"10.0.0.8","connect_ip":"198.51.100.8"}`},
		{"null item", `[null]`},
		{"missing connection IP", `[{"listen_ip":"10.0.0.8"}]`},
		{"wildcard", `[{"listen_ip":"0.0.0.0","connect_ip":"198.51.100.8"}]`},
		{"loopback", `[{"listen_ip":"10.0.0.8","connect_ip":"::ffff:127.0.0.1"}]`},
		{"multicast", `[{"listen_ip":"ff02::1","connect_ip":"198.51.100.8"}]`},
		{"host name", `[{"listen_ip":"10.0.0.8","connect_ip":"relay.example"}]`},
		{"port", `[{"listen_ip":"10.0.0.8","connect_ip":"198.51.100.8:443"}]`},
		{"network", `[{"listen_ip":"10.0.0.0/24","connect_ip":"198.51.100.8"}]`},
		{"interface zone", `[{"listen_ip":"2001:db8::8%eth0","connect_ip":"198.51.100.8"}]`},
		{"mapped IPv4 interface zone", `[{"listen_ip":"::ffff:10.0.0.8%eth0","connect_ip":"198.51.100.8"}]`},
		{"duplicate bind IP", `[
			{"listen_ip":"10.0.0.8","connect_ip":"198.51.100.8"},
			{"listen_ip":"::ffff:10.0.0.8","connect_ip":"198.51.100.9"}
		]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"name":"mapped","address_mappings":` + test.raw + `}`
			f.request(t, "POST", "/nodes", body, 400)
			f.request(t, "PUT", path, body, 400)
		})
	}
	stored := handlerData[model.GostNode](t, f.request(t, "GET", path, "", 200))
	if !slices.Equal(stored.AddressMappings, node.AddressMappings) {
		t.Fatalf("rejected requests modified the persisted mappings: %+v", stored)
	}
}
