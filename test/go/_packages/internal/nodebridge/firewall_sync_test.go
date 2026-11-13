package nodebridge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"yagp/pkg/nodegrpc"
)

func TestFirewallReadsManagedGostListeners(t *testing.T) {
	const services = `{"services":[
		{"name":"rule-1","addr":":20001","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"running"}},
		{"name":"rule-2","addr":"192.0.2.10:20002","handler":{"type":"forward"},"listener":{"type":"udp"},"status":{"state":"ready"}},
		{"name":"relay-tunnel-1","addr":"[2001:db8::10]:20003","handler":{"type":"relay"},"listener":{"type":"grpc"},"status":{"state":"running"}},
		{"name":"relay-tunnel-2","addr":":20004","handler":{"type":"relay"},"listener":{"type":"h3"},"status":{"state":"running"}},
		{"name":"relay-tunnel-3","addr":":20005","handler":{"type":"relay"},"listener":{"type":"kcp","metadata":{"kcp.tcp":"true"}},"status":{"state":"running"}},
		{"name":"relay-tunnel-4","addr":":20006","handler":{"type":"relay"},"listener":{"type":"kcp"},"status":{"state":"running"}},
		{"name":"relay-tunnel-5","addr":"[::ffff:192.0.2.11]:20007","handler":{"type":"relay"},"listener":{"type":"quic"},"status":{"state":"running"}},
		{"name":"custom","addr":":30001","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"running"}},
		{"name":"rule-3","addr":"127.0.0.1:30002","handler":{"type":"red"},"listener":{"type":"red"},"status":{"state":"running"}},
		{"name":"rule-4","addr":"[::1]:30003","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"running"}},
		{"name":"rule-5","addr":":30004","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"failed"}},
		{"name":"rule-6","addr":":30005","handler":{"type":"forward"},"listener":{"type":"tcp"}},
		{"name":"rule-7","addr":":70000","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"running"}},
		{"name":"rule-8","addr":"not-an-ip:30008","handler":{"type":"forward"},"listener":{"type":"tcp"},"status":{"state":"running"}},
		{"name":"relay-tunnel-6","addr":":30009","handler":{"type":"relay"},"listener":{"type":"unknown"},"status":{"state":"running"}}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "daemon" || password != "secret" || r.URL.Path != "/api/config" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprint(w, services)
	}))
	defer server.Close()
	control := firewallTestControl(server)
	got, err := control.firewallPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := FirewallPorts{
		TCP: []uint16{20001, 20005}, UDP: []uint16{20004, 20006},
		TCPAddresses: []FirewallEndpoint{{Address: netip.MustParseAddr("2001:db8::10"), Port: 20003}},
		UDPAddresses: []FirewallEndpoint{
			{Address: netip.MustParseAddr("192.0.2.10"), Port: 20002},
			{Address: netip.MustParseAddr("192.0.2.11"), Port: 20007},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("firewall ports = %+v, want %+v", got, want)
	}
}

func TestFirewallSyncRecordsGostReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"error":"configuration unavailable"}`)
	}))
	defer server.Close()
	control := firewallTestControl(server)
	manager, err := NewFirewallManager(42, FirewallConfig{Table: "host", InputChain: "input"})
	if err != nil {
		t.Fatal(err)
	}
	control.firewall = manager
	control.syncFirewall(context.Background())
	state := manager.Status()
	if state.Synchronized || state.LastError == nil || !strings.Contains(*state.LastError, "missing services") {
		t.Fatalf("firewall status = %+v", state)
	}
}

func TestFirewallReconcileTriggeredBySuccessfulGostServiceChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/config/services/rule-2" {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	control := firewallTestControl(server)
	control.firewallChanged = make(chan struct{}, 1)
	for _, change := range []struct {
		method, path string
		trigger      bool
	}{
		{http.MethodPost, "/config/services", true},
		{http.MethodDelete, "/config/services/rule-1", true},
		{http.MethodDelete, "/config/services/rule-2", false},
		{http.MethodGet, "/config/services/rule-1", false},
		{http.MethodPost, "/config/chains", false},
	} {
		if _, err := control.Execute(context.Background(), &nodegrpc.CommandRequest{Method: change.method, Path: change.path}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-control.firewallChanged:
			if !change.trigger {
				t.Errorf("unexpected reconcile trigger for %s %s", change.method, change.path)
			}
		default:
			if change.trigger {
				t.Errorf("missing reconcile trigger for %s %s", change.method, change.path)
			}
		}
	}
}

func firewallTestControl(server *httptest.Server) *controlServer {
	return &controlServer{
		config: Config{AdminAddress: server.Listener.Addr().String(), Username: "daemon", Password: "secret"},
		client: server.Client(),
	}
}
