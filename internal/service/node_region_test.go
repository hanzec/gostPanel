package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"yagp/internal/model"
	"yagp/internal/repository"
)

type regionTransport func(*http.Request) (*http.Response, error)

func (f regionTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestNodeRegionsBackfillAndRefreshAfterIPChange(t *testing.T) {
	db := newServiceTestDB(t)
	node := model.GostNode{Name: "node", Address: "192.0.2.7", IPv4: "192.0.2.7", IPv6: "2001:db8::7"}
	saveServiceFixtures(t, db, &node)
	var paths []string
	client := &http.Client{Transport: regionTransport(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		body := map[string]string{
			"/v1/ip/country/192.0.2.7":   "",
			"/v1/ip/country/2001:db8::7": "HK",
			"/v1/ip/country/192.0.2.8":   "JP",
		}[req.URL.Path]
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	nodes := repository.NewNodeRepository(db)
	for _, want := range []string{"HK", "JP"} {
		if err := refreshNodeRegions(t.Context(), db, client); err != nil {
			t.Fatal(err)
		}
		current, err := nodes.FindByID(node.ID)
		if err != nil || current.Region != want {
			t.Fatalf("node region = %+v, %v, want %q", current, err, want)
		}
		if want == "HK" {
			if err := nodes.UpdateIPs(t.Context(), current, "192.0.2.8", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if strings.Join(paths, ",") != "/v1/ip/country/192.0.2.7,/v1/ip/country/2001:db8::7,/v1/ip/country/192.0.2.8" {
		t.Fatalf("region lookup order = %v", paths)
	}
}
