package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/pkg/geoip"
)

type regionLookupTransport func(*http.Request) (*http.Response, error)

func (call regionLookupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return call(req)
}

func TestNodeHTTPReturnsRegionFromCurrentIP(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.geoIP = geoip.NewDatabase("../../pkg/geoip/testdata/GeoIP2-Country-Test.mmdb", "")
	h := f.registerNodes()
	h.geoIPClient = &http.Client{Transport: regionLookupTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected online lookup with a readable database: %s", req.URL)
		return nil, nil
	})}

	// Older deployments stored the region on the node record.
	if err := f.db.Exec("ALTER TABLE nodes ADD COLUMN region VARCHAR(2) NOT NULL DEFAULT 'CA'").Error; err != nil {
		t.Fatal(err)
	}
	node := model.GostNode{Name: "edge", Address: "192.0.2.7", IPv4: "192.0.2.7", IPv6: "2001:218::1"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/nodes/%d", node.ID)
	assertRegion := func(reply handlerReply, want string) {
		t.Helper()
		result := handlerData[model.GostNode](t, reply)
		if result.Region != want {
			t.Fatalf("node region = %q, want %q: %s", result.Region, want, reply.Data)
		}
	}
	assertRegion(f.request(t, http.MethodGet, path, "", http.StatusOK), "JP")

	if err := repository.NewNodeRepository(f.db).UpdateIPs(t.Context(), &node, "81.2.69.142", node.IPv6, nil, nil); err != nil {
		t.Fatal(err)
	}
	assertRegion(f.request(t, http.MethodGet, path, "", http.StatusOK), "GB")
	assertRegion(f.request(t, http.MethodPut, path, `{"name":"updated"}`, http.StatusOK), "GB")
	page := handlerData[handlerPage[model.GostNode]](t, f.request(t, http.MethodGet, "/nodes", "", http.StatusOK))
	if len(page.List) != 1 || page.List[0].Region != "GB" {
		t.Fatalf("node list does not contain the current region: %+v", page)
	}
}

func TestNodeHTTPFallsBackToGeoJSWhenRegionDatabaseDownloadFails(t *testing.T) {
	var downloads atomic.Int32
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer download.Close()

	f := newHandlerHTTPFixture(t)
	f.geoIP = geoip.NewDatabase(filepath.Join(t.TempDir(), "country.mmdb"), download.URL)
	h := f.registerNodes()
	h.geoIPClient = &http.Client{Transport: regionLookupTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Host != "get.geojs.io" {
			t.Fatalf("unexpected GeoJS request: %s %s", req.Method, req.URL)
		}
		status := http.StatusOK
		var country string
		switch req.URL.Path {
		case "/v1/ip/country/81.2.69.142":
			country = "GB\n"
		case "/v1/ip/country/2001:218::1":
			country = "JP\n"
		case "/v1/ip/country/192.0.2.7":
			status = http.StatusServiceUnavailable
		default:
			t.Fatalf("unexpected GeoJS request path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(country))}, nil
	})}

	nodes := []model.GostNode{
		{Name: "london", Address: "81.2.69.142", IPv4: "81.2.69.142"},
		{Name: "tokyo", Address: "10.0.0.1", IPv4: "10.0.0.1", IPv6: "2001:218::1"},
		{Name: "unknown", Address: "192.0.2.7", IPv4: "192.0.2.7"},
	}
	if err := f.db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	page := handlerData[handlerPage[model.GostNode]](t, f.request(t, http.MethodGet, "/nodes", "", http.StatusOK))
	if downloads.Load() != 1 || len(page.List) != len(nodes) {
		t.Fatalf("GeoIP attempts = %d, node list = %+v", downloads.Load(), page)
	}
	want := map[string]string{"london": "GB", "tokyo": "JP", "unknown": ""}
	for _, node := range page.List {
		if node.Region != want[node.Name] {
			t.Errorf("node %q region = %q, want %q", node.Name, node.Region, want[node.Name])
		}
	}
}
