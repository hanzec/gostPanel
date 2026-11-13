package nodebridge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	auth "github.com/go-gost/plugin/auth/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSharedRelayKeepsCredentialsAndSourcesPaired(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "node.yaml")
	control := &controlServer{config: Config{ConfigFile: configFile}}
	const service = "relay-tunnel-42"
	request := func(method, path, body string) (*nodegrpc.CommandReply, error) {
		return control.Execute(t.Context(), &nodegrpc.CommandRequest{Method: method, Path: path, Body: []byte(body)})
	}
	path := nodegrpc.RelayAuthPathPrefix + service
	policy := nodegrpc.RelayAuthConfig{Users: []nodegrpc.RelayAuthUser{
		{Username: "first", Password: "test-first", RestrictSources: true, AllowedIPs: []string{"192.0.2.1", "::ffff:192.0.2.1"}},
		{Username: "second", Password: "test-second", RestrictSources: true, AllowedIPs: []string{"2001:db8::2"}},
		{Username: "blocked", Password: "test-blocked", RestrictSources: true},
		{Username: "unrestricted", Password: "test-unrestricted"},
	}}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{nodegrpc.RelayAuthPathPrefix + "../node.yaml", nodegrpc.RelayAuthPathPrefix + "relay-tunnel-0"} {
		if _, err := request(http.MethodPut, invalid, string(body)); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid service %q accepted: %v", invalid, err)
		}
	}
	getConfigured := func(want bool) {
		t.Helper()
		reply, err := request(http.MethodGet, path, "")
		expected, _ := json.Marshal(nodegrpc.RelayAuthStatus{Configured: want})
		if err != nil || reply.GetStatusCode() != http.StatusOK || string(reply.GetBody()) != string(expected) {
			t.Fatalf("GET relay auth config = %v, %v, want %s", reply, err, expected)
		}
	}
	putPolicy := func(data string) {
		t.Helper()
		if reply, err := request(http.MethodPut, path, data); err != nil || reply.GetStatusCode() != http.StatusOK {
			t.Fatalf("PUT relay auth config = %v, %v", reply, err)
		}
	}
	getConfigured(false)
	putPolicy(string(body))
	getConfigured(true)
	if info, err := os.Stat(relayAuthPolicyPath(configFile, service)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected relay auth file metadata: %v, %v", info, err)
	}
	if _, err := request(http.MethodPut, path, `{"users":[{"username":"first","password":"test-first","allowed_ips":["invalid"]}]}`); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid source IP update accepted: %v", err)
	}

	const token = "local-relay-test-token"
	listener := listenLocal(t)
	server := grpc.NewServer()
	auth.RegisterAuthenticatorServer(server, &relayAuthServer{configFile: configFile, token: token})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := auth.NewAuthenticatorClient(conn)
	ctx := metadata.AppendToOutgoingContext(t.Context(), "token", token)
	if _, err := client.Authenticate(t.Context(), &auth.AuthenticateRequest{Service: service}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("local plugin authentication without token = %v", err)
	}
	check := func(ctx context.Context, service, user, pass, address, want string) {
		t.Helper()
		reply, err := client.Authenticate(ctx, &auth.AuthenticateRequest{
			Service: service, Username: user, Password: pass, Client: address,
		})
		if err != nil || reply.GetOk() != (want != "") || reply.GetId() != want {
			t.Fatalf("user %s from %s = %v, %v, want %q", user, address, reply, err, want)
		}
	}
	check(ctx, service, "first", "test-first", "192.0.2.1:10080", "first")
	check(ctx, service, "first", "test-first", "[::ffff:192.0.2.1]:10080", "first")
	check(ctx, service, "second", "test-second", "[2001:db8::2]:10080", "second")
	check(ctx, service, "first", "test-first", "[2001:db8::2]:10080", "")
	check(ctx, service, "first", "test-second", "192.0.2.1:10080", "")
	check(ctx, service, "second", "test-second", "192.0.2.1:10080", "")
	check(ctx, service, "blocked", "test-blocked", "192.0.2.1:10080", "")
	check(ctx, service, "unrestricted", "test-unrestricted", "192.0.2.1:10080", "unrestricted")
	check(ctx, "relay-tunnel-43", "first", "test-first", "192.0.2.1:10080", "")

	body, err = json.Marshal(nodegrpc.RelayAuthConfig{Users: policy.Users[1:2]})
	if err != nil {
		t.Fatal(err)
	}
	putPolicy(string(body))
	check(ctx, service, "first", "test-first", "192.0.2.1:10080", "")
	check(ctx, service, "second", "test-second", "[2001:db8::2]:10080", "second")
	for range 2 {
		if reply, err := request(http.MethodDelete, path, ""); err != nil || reply.GetStatusCode() != http.StatusOK {
			t.Fatalf("DELETE relay auth config = %v, %v", reply, err)
		}
	}
	getConfigured(false)
	check(ctx, service, "second", "test-second", "[2001:db8::2]:10080", "")
}

func TestOnlyManagedEntrancesAllowServiceUpdates(t *testing.T) {
	paths := []string{"rule-42", "rule-42-docker", "rule-0", "rule-42-backup", "custom"}
	var allowed []string
	for _, name := range paths {
		if allowedCommand(http.MethodPut, "/config/services/"+name) {
			allowed = append(allowed, name)
		}
	}
	if want := []string{"rule-42", "rule-42-docker"}; !slices.Equal(allowed, want) {
		t.Fatalf("managed service updates = %v, want %v", allowed, want)
	}
}

func TestOnlyManagedRelayAutherResourcesAreAllowed(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
		allow  bool
	}{
		{http.MethodPost, "/config/authers", true},
		{http.MethodGet, "/config/authers", false},
		{http.MethodGet, "/config/authers/relay-tunnel-42", true},
		{http.MethodGet, "/config/authers/relay-tunnel-0", false},
		{http.MethodGet, "/config/authers/custom", false},
		{http.MethodPut, "/config/authers/relay-tunnel-42", false},
		{http.MethodDelete, "/config/authers/relay-tunnel-42", true},
		{http.MethodDelete, "/config/authers/custom", false},
	} {
		if got := allowedCommand(tc.method, tc.path); got != tc.allow {
			t.Errorf("%s %s allowed = %v, want %v", tc.method, tc.path, got, tc.allow)
		}
	}
}

func TestRelayAutherCanOnlyTargetTheLocalPlugin(t *testing.T) {
	control := &controlServer{config: Config{ObserverListen: "127.0.0.1:5050", Username: "node", Password: "test-node-password"}}
	for _, body := range []string{
		`{"name":"custom","plugin":{"type":"grpc","addr":"127.0.0.1:5050"}}`,
		`{"name":"relay-tunnel-1","plugin":{"type":"grpc","addr":"192.0.2.1:5050"}}`,
		`{"name":"relay-tunnel-1","plugin":{"type":"grpc","addr":"127.0.0.1:5050","command":["unused"]}}`,
		`{"name":"relay-tunnel-1","plugin":{"type":"grpc","addr":"127.0.0.1:5050","token":"wrong"}}`,
	} {
		if _, err := control.Execute(t.Context(), &nodegrpc.CommandRequest{
			Method: http.MethodPost, Path: "/config/authers", Body: []byte(body),
		}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid Relay authenticator accepted: %v", err)
		}
	}
}

func TestSharedRelayAuthenticationThroughGost(t *testing.T) {
	binary := os.Getenv("YAGP_TEST_GOST_BINARY")
	if binary == "" || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") {
		t.Skip("set YAGP_TEST_GOST_BINARY to an installed Linux or macOS Gost binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "gost")); err != nil {
		t.Fatal(err)
	}
	const service = "relay-tunnel-1"
	relayAddress, adminAddress := tunnelProbeTestAddresses(t)
	control := &controlServer{config: Config{
		AdminAddress: adminAddress, ConfigFile: filepath.Join(dir, "node.yaml"),
		Username: "node", Password: "node-password",
	}, client: &http.Client{}}
	defer control.client.CloseIdleConnections()

	listener := listenLocal(t)
	control.config.ObserverListen = listener.Addr().String()
	token := nodegrpc.ObserverToken(control.config.Username, control.config.Password)
	server := grpc.NewServer()
	auth.RegisterAuthenticatorServer(server, &relayAuthServer{configFile: control.config.ConfigFile, token: token})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	chain := gost.ChainConfig{Name: "tunnel-1-chain", Hops: []*gost.HopConfig{{Name: "hop-0", Nodes: []*gost.NodeConfig{{
		Name: "exit-relay", Addr: relayAddress,
		Connector: &gost.ConnectorConfig{Type: "relay", Auth: &gost.AuthConfig{Username: "first", Password: "first-password"}},
		Dialer:    gost.RelayDialer("ws", map[string]string{"path": "/probe"}),
	}}}}}
	relay := gost.ServiceConfig{Name: service, Addr: relayAddress,
		Listener: gost.RelayListener("ws", map[string]string{"path": "/probe"}),
		Handler:  &gost.HandlerConfig{Type: "relay", Auth: chain.Hops[0].Nodes[0].Connector.Auth},
	}
	startTunnelProbeTestRelay(t, binary, dir, relay, chain, control)
	put := func(method, path string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := control.Execute(t.Context(), &nodegrpc.CommandRequest{Method: method, Path: path, Body: body})
		if err != nil || reply.GetStatusCode() != http.StatusOK {
			t.Fatalf("%s %s failed: %v, %s", method, path, err, reply.GetBody())
		}
	}
	first := nodegrpc.RelayAuthUser{Username: "first", Password: "first-password", RestrictSources: true, AllowedIPs: []string{"127.0.0.1"}}
	second := nodegrpc.RelayAuthUser{Username: "second", Password: "second-password", RestrictSources: true, AllowedIPs: []string{"127.0.0.1"}}
	rejected := nodegrpc.RelayAuthUser{Username: "rejected", Password: "rejected-password", RestrictSources: true, AllowedIPs: []string{"192.0.2.1"}}
	put(http.MethodPut, nodegrpc.RelayAuthPathPrefix+service, nodegrpc.RelayAuthConfig{Users: []nodegrpc.RelayAuthUser{first, second, rejected}})
	put(http.MethodPost, "/config/authers", map[string]any{
		"name":   service,
		"plugin": gost.PluginConfig{Type: "grpc", Addr: listener.Addr().String(), Token: token},
	})
	relay.Handler.Auth = nil
	relay.Handler.Auther = service
	put(http.MethodPut, "/config/services/"+service, relay)

	probe := func(user nodegrpc.RelayAuthUser, allowed bool) {
		t.Helper()
		chain.Hops[0].Nodes[0].Connector.Auth = &gost.AuthConfig{Username: user.Username, Password: user.Password}
		put(http.MethodPut, "/config/chains/"+chain.Name, chain)
		endpoint, err := control.StartTunnelProbe(t.Context(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = control.ProbeTunnel(t.Context(), &nodegrpc.TunnelProbeRequest{
			ChainName: chain.Name, Address: endpoint.GetAddress(), Token: endpoint.GetToken(),
		})
		if allowed {
			if err != nil {
				t.Fatalf("relay rejected allowed user %s: %v", user.Username, err)
			}
		} else if info := requireTunnelProbeFailure(t, err, codes.Unavailable, "relay_read"); info.GetMetadata()["gost_error"] != "relay_authentication" {
			t.Fatalf("relay rejection for %s: %v", user.Username, err)
		}
	}
	probe(first, true)
	probe(second, true)
	probe(rejected, false)
	put(http.MethodPut, nodegrpc.RelayAuthPathPrefix+service, nodegrpc.RelayAuthConfig{Users: []nodegrpc.RelayAuthUser{second}})
	probe(first, false)
	probe(second, true)
}
