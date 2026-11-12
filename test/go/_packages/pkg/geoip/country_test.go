package geoip

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

type countryTransport struct {
	origin *url.URL
	next   http.RoundTripper
}

func (tr countryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.Host = req.URL.Host
	local.URL.Scheme, local.URL.Host = tr.origin.Scheme, tr.origin.Host
	return tr.next.RoundTrip(local)
}

func TestCountryLooksUpEgressAndPublicAddresses(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "get.geojs.io" || r.Method != http.MethodGet {
			t.Errorf("unexpected lookup %s %s", r.Method, r.Host)
		}
		switch r.URL.Path {
		case "/v1/ip/country":
			_, _ = io.WriteString(w, "CN\n")
		case "/v1/ip/country/2001:db8::42":
			_, _ = io.WriteString(w, "HK\r\n")
		case "/v1/ip/country/192.0.2.2":
			_, _ = io.WriteString(w, "invalid code")
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(server.Close)
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Transport = countryTransport{origin, client.Transport}
	for _, tc := range []struct {
		ip   netip.Addr
		want string
	}{
		{netip.Addr{}, "CN"},
		{netip.MustParseAddr("2001:db8::42"), "HK"},
		{netip.MustParseAddr("192.0.2.2"), ""},
		{netip.MustParseAddr("192.0.2.3"), ""},
	} {
		if got := Country(t.Context(), client, tc.ip); got != tc.want {
			t.Errorf("Country(%s) = %q, want %q", tc.ip, got, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := Country(ctx, client, netip.Addr{}); code != "" {
		t.Fatalf("canceled lookup = %q", code)
	}
}
