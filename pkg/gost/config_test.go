package gost

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestObserverChangesOnlyWhenPluginChanges(t *testing.T) {
	client, calls := controlFixture(t)
	observer := &ObserverConfig{Name: "metrics", Plugin: &PluginConfig{Type: "grpc", Addr: "127.0.0.1:2000", Token: "new"}}
	const collection = "/config/observers"
	const resource = collection + "/metrics"
	const body = `{"name":"metrics","plugin":{"type":"grpc","addr":"127.0.0.1:2000","token":"new"}}`
	missing := gostCommand("GET", resource, "", 200, `{"data":null}`)
	same := gostCommand("GET", resource, "", 200, `{"data":`+body+`}`)
	old := gostCommand("GET", resource, "", 200, `{"data":{"name":"metrics","plugin":{"type":"grpc","addr":"127.0.0.1:2000","token":"old"}}}`)
	for _, scenario := range []struct {
		name  string
		steps []controlCall
		fail  bool
	}{
		{"create", []controlCall{missing, gostCommand("POST", collection, body, 201, "{}")}, false},
		{"unchanged", []controlCall{same}, false},
		{"rotate token", []controlCall{old, gostCommand("PUT", resource, body, 200, "{}")}, false},
		{"rejected update", []controlCall{old, gostCommand("PUT", resource, body, 409, "{}")}, true},
		{"bad response wrapper", []controlCall{gostCommand("GET", resource, "", 200, "invalid")}, true},
		{"wrong resource shape", []controlCall{gostCommand("GET", resource, "", 200, `{"data":[]}`)}, true},
		{"unavailable lookup", []controlCall{gostCommand("GET", resource, "", 503, "unavailable")}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			for _, step := range scenario.steps {
				calls <- step
			}
			if err := client.SetObserver(observer); (err != nil) != scenario.fail {
				t.Fatalf("set observer = %v, want error %t", err, scenario.fail)
			}
		})
	}
	calls <- same
	calls <- gostCommand("DELETE", resource, "", 204, "")
	if err := client.DeleteObserver("metrics"); err != nil {
		t.Fatal(err)
	}
	calls <- missing
	if err := client.DeleteObserver("metrics"); err != nil {
		t.Fatal(err)
	}
	calls <- same
	calls <- gostCommand("DELETE", resource, "", 403, "observer in use")
	if err := client.DeleteObserver("metrics"); err == nil || !strings.Contains(err.Error(), "observer in use") {
		t.Fatalf("delete observer = %v", err)
	}
	calls <- gostCommand("GET", resource, "", 503, "")
	if err := client.DeleteObserver("metrics"); err == nil {
		t.Fatal("delete proceeded after lookup failed")
	}
	calls <- controlCall{method: nodegrpc.V1_GetObserverEndpoint_FullMethodName, request: &emptypb.Empty{}, response: &nodegrpc.ObserverEndpoint{Address: "127.0.0.1:2000"}}
	if addr, err := client.ObserverAddress(); err != nil || addr != observer.Plugin.Addr {
		t.Fatalf("observer address = %q, %v", addr, err)
	}
	calls <- controlCall{method: nodegrpc.V1_GetObserverEndpoint_FullMethodName, request: &emptypb.Empty{}, err: status.Error(codes.PermissionDenied, "observer disabled")}
	if _, err := client.ObserverAddress(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("disabled observer = %v", err)
	}
	if _, err := NewClient(&Config{}).ObserverAddress(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unregistered observer = %v", err)
	}
	if client.ObserverToken() != nodegrpc.ObserverToken("gost-test", "fixture-password") {
		t.Fatal("unexpected observer token")
	}
}

func TestGostAllowlistAndBypassUpdates(t *testing.T) {
	client, calls := controlFixture(t)
	admission := AdmissionConfig{Name: RelayAllowlistName, Whitelist: true, Matchers: []string{"192.0.2.1"}}
	const admissionJSON = `{"name":"yagp-relay-allowlist","whitelist":true,"matchers":["192.0.2.1"]}`
	calls <- gostCommand("POST", "/config/admissions", admissionJSON, http.StatusCreated, "{}")
	if changed, err := client.EnsureAdmission(admission, []AdmissionConfig{{Name: "unrelated"}}); err != nil || !changed {
		t.Fatalf("create admission = %t, %v", changed, err)
	}
	if changed, err := client.EnsureAdmission(admission, []AdmissionConfig{admission}); err != nil || changed {
		t.Fatalf("unchanged admission = %t, %v", changed, err)
	}
	const admissionPath = "/config/admissions/" + RelayAllowlistName
	for _, code := range []int{http.StatusOK, http.StatusBadRequest} {
		calls <- gostCommand("PUT", admissionPath, admissionJSON, code, "invalid allowlist")
		changed, err := client.EnsureAdmission(admission, []AdmissionConfig{{Name: admission.Name}})
		if changed != (code == http.StatusOK) || (err != nil) != (code != http.StatusOK) {
			t.Fatalf("update admission with HTTP %d = %t, %v", code, changed, err)
		}
	}
	const relayPath = "/config/services/relay"
	calls <- gostCommand("GET", relayPath, "", 200, `{"data":{"name":"relay","handler":{"type":"relay","extra":true},"admissions":["custom"],"status":{"state":"ready"},"private":"preserve"}}`)
	calls <- gostCommand("PUT", relayPath, `{"name":"relay","handler":{"type":"relay","extra":true},"admissions":["custom","yagp-relay-allowlist"],"private":"preserve"}`, 200, "{}")
	if changed, err := client.AttachRelayAllowlist("relay"); err != nil || !changed {
		t.Fatalf("attach relay admission = %t, %v", changed, err)
	}
	for _, body := range []string{
		`{"data":null}`,
		`{"data":{"name":"relay","handler":{"type":"forward"}}}`,
		`{"data":{"name":"relay","handler":{"type":"relay"},"admission":"yagp-relay-allowlist"}}`,
	} {
		calls <- gostCommand("GET", relayPath, "", 200, body)
		if changed, err := client.AttachRelayAllowlist("relay"); err != nil || changed {
			t.Fatalf("no-op admission = %t, %v for %s", changed, err, body)
		}
	}
	calls <- gostCommand("GET", relayPath, "", 200, `{"data":[]}`)
	if _, err := client.AttachRelayAllowlist("relay"); err == nil {
		t.Fatal("accepted an invalid relay service response")
	}

	const bypassPath = "/config/bypasses/" + TransparentBypassName
	const bypassJSON = `{"name":"yagp-transparent-proxy","whitelist":true,"matchers":[".example.test"]}`
	matchers := []string{".example.test"}
	for _, scenario := range []struct {
		current       string
		steps         []controlCall
		changed, fail bool
	}{
		{`{"data":null}`, []controlCall{gostCommand("POST", "/config/bypasses", bypassJSON, 201, "{}")}, true, false},
		{`{"data":` + bypassJSON + `}`, nil, false, false},
		{`{"data":{"name":"yagp-transparent-proxy"}}`, []controlCall{gostCommand("PUT", bypassPath, bypassJSON, 200, "{}")}, true, false},
		{`{"data":{"name":"yagp-transparent-proxy"}}`, []controlCall{gostCommand("PUT", bypassPath, bypassJSON, 403, "")}, false, true},
		{`{"data":[]}`, nil, false, true},
	} {
		calls <- gostCommand("GET", bypassPath, "", 200, scenario.current)
		for _, step := range scenario.steps {
			calls <- step
		}
		changed, err := client.SetTransparentBypass(matchers)
		if changed != scenario.changed || (err != nil) != scenario.fail {
			t.Fatalf("set bypass for %s = %t, %v", scenario.current, changed, err)
		}
	}
	calls <- gostCommand("GET", bypassPath, "", 503, "")
	if _, err := client.SetTransparentBypass(matchers); err == nil {
		t.Fatal("continued after a failed bypass lookup")
	}
}

func TestTunnelRelayUpdatePreservesGostFields(t *testing.T) {
	client, calls := controlFixture(t)
	const path = "/config/chains/tunnel"
	const original = `{"name":"tunnel","metadata":{"retry":2},"hops":[null,{"name":"other"},{"name":"hop-0","nodes":[null,{"name":"exit-relay","addr":"192.0.2.1:443","connector":{"type":"relay","custom":true}}]}]}`
	var chain ChainConfig
	if err := json.Unmarshal([]byte(original), &chain); err != nil {
		t.Fatal(err)
	}
	if addr, ok := chain.TunnelRelayAddress(); !ok || addr != "192.0.2.1:443" {
		t.Fatalf("tunnel relay = %q, %t", addr, ok)
	}
	if _, ok := (ChainConfig{Hops: []*HopConfig{{Name: "hop-0"}}}).TunnelRelayAddress(); ok {
		t.Fatal("chain with no relay reported a relay address")
	}
	const host = "2001:db8::7"
	updated := strings.Replace(original, "192.0.2.1:443", "[2001:db8::7]:443", 1)
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		calls <- gostCommand("GET", path, "", 200, `{"data":`+original+`}`)
		calls <- gostCommand("PUT", path, updated, code, "node unavailable")
		changed, err := client.UpdateTunnelRelayHost("tunnel", host)
		if changed != (code == http.StatusOK) || (err != nil) != (code != http.StatusOK) {
			t.Fatalf("retarget with HTTP %d = %t, %v", code, changed, err)
		}
	}
	for _, scenario := range []struct {
		data string
		fail bool
	}{
		{"null", false}, {updated, false}, {`[]`, true}, {`{}`, false},
		{`{"hops":{}}`, true}, {`{"hops":[{"name":1}]}`, true},
		{`{"hops":[{"name":"hop-0","nodes":[{"name":"other"}]}]}`, false},
		{`{"hops":[{"name":"hop-0","nodes":[{"name":"exit-relay"}]}]}`, false},
		{`{"hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":2}]}]}`, true},
		{`{"hops":[{"name":"hop-0","nodes":[{"name":"exit-relay","addr":"missing-port"}]}]}`, false},
	} {
		calls <- gostCommand("GET", path, "", 200, `{"data":`+scenario.data+`}`)
		if changed, err := client.UpdateTunnelRelayHost("tunnel", host); changed || (err != nil) != scenario.fail {
			t.Fatalf("retarget for %s = %t, %v", scenario.data, changed, err)
		}
	}
}
