package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/pkg/geoip"
	"yagp/pkg/logger"
)

type regionLookupTransport func(*http.Request) (*http.Response, error)

func (call regionLookupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return call(req)
}

func TestNodeHTTPReturnsRegionFromCurrentIP(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.geoIP = geoip.NewDatabase("../../test/go/fixtures/geoip/GeoIP2-Country-Test.mmdb", "")
	if err := f.geoIP.Load(); err != nil {
		t.Fatal(err)
	}
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

	if err := repository.NewNodeRepository(f.db).UpdateIPs(t.Context(), &node, "81.2.69.142", node.IPv6, nil, nil, false); err != nil {
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
	if err := f.geoIP.Refresh(t.Context()); err == nil {
		t.Fatal("unavailable database download succeeded")
	}
	logFile, err := os.CreateTemp(t.TempDir(), "geoip-warning-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = logger.Init(&logger.Config{Level: "error"})
		_ = logFile.Close()
	})
	stdout := os.Stdout
	os.Stdout = logFile
	err = logger.Init(&logger.Config{Level: "warn"})
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
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
	_ = logger.Sync()
	logs, err := os.ReadFile(logFile.Name())
	if err != nil || !strings.Contains(string(logs), `"level":"warn"`) ||
		!strings.Contains(string(logs), "回退到旧版 GeoJS 在线查询") {
		t.Fatalf("missing fallback warning: %s, %v", logs, err)
	}
	f.request(t, http.MethodGet, "/nodes", "", http.StatusOK)
	if downloads.Load() != 1 {
		t.Fatalf("list request retried the database download: %d attempts", downloads.Load())
	}
}

func TestNodeListDoesNotWaitForGeoIPDownload(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishDownload := func() { releaseOnce.Do(func() { close(release) }) }
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		http.ServeFile(w, r, "../../test/go/fixtures/geoip/GeoIP2-Country-Test.mmdb")
	}))
	defer download.Close()
	defer finishDownload()
	f := newHandlerHTTPFixture(t)
	f.geoIP = geoip.NewDatabase(filepath.Join(t.TempDir(), "country.mmdb"), download.URL)
	refreshed := make(chan error, 1)
	go func() { refreshed <- f.geoIP.Refresh(t.Context()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("GeoIP download did not start")
	}
	h := f.registerNodes()
	var onlineLookups atomic.Int32
	h.geoIPClient = &http.Client{Transport: regionLookupTransport(func(req *http.Request) (*http.Response, error) {
		onlineLookups.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("GB\n"))}, nil
	})}
	if err := f.db.Create(&model.GostNode{Name: "edge", IPv4: "81.2.69.142"}).Error; err != nil {
		t.Fatal(err)
	}
	requested := time.Now()
	page := handlerData[handlerPage[model.GostNode]](t, f.request(t, http.MethodGet, "/nodes", "", http.StatusOK))
	if elapsed := time.Since(requested); elapsed > time.Second {
		t.Fatalf("node list waited for the database download: %s", elapsed)
	}
	if len(page.List) != 1 || page.List[0].Region != "GB" || onlineLookups.Load() != 1 {
		t.Fatalf("online fallback while database downloads: %+v, lookups=%d", page, onlineLookups.Load())
	}
	finishDownload()
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	page = handlerData[handlerPage[model.GostNode]](t, f.request(t, http.MethodGet, "/nodes", "", http.StatusOK))
	if len(page.List) != 1 || page.List[0].Region != "GB" || onlineLookups.Load() != 1 {
		t.Fatalf("completed database was not reused: %+v, lookups=%d", page, onlineLookups.Load())
	}
}
