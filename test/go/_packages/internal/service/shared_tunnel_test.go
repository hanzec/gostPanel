package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"
)

func TestEndpointTunnelsShareAnExitRelay(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	firstRemote, secondRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	exitRemote := &sharedRelayTestNode{lifecycleNode: lifecycleNode{t: t}, policies: make(map[string]nodegrpc.RelayAuthConfig)}
	first := startServiceTestNode(t, firstRemote)
	second := startServiceTestNode(t, secondRemote)
	exit := startServiceTestNode(t, exitRemote)
	first.Name, second.Name, exit.Name = "first entry", "second entry", "shared exit"
	exit.Address = "198.51.100.21"
	saveServiceFixtures(t, db, &first, &second, &exit)
	firstEntry := model.GostRule{Name: "first source", NodeID: &first.ID, Role: model.EndpointRoleEntry, Protocol: model.RuleProtocolTCP, ListenPort: 18001}
	secondEntry := model.GostRule{Name: "second source", NodeID: &second.ID, Role: model.EndpointRoleEntry, Protocol: model.RuleProtocolTCP, ListenPort: 18002}
	sharedExit := model.GostRule{Name: "shared destination", NodeID: &exit.ID, Role: model.EndpointRoleExit, Protocol: "tcp", ListenPort: 18443, Targets: []string{"app.test:443"}}
	saveServiceFixtures(t, db, &firstEntry, &secondEntry, &sharedExit)
	tunnels := NewTunnelService(db)

	left := startSharedTunnel(t, tunnels, "first path", firstEntry.ID, sharedExit.ID)
	secondRemote.mu.Lock()
	secondRemote.reject = map[string]int{"POST /config/chains": http.StatusBadGateway}
	secondRemote.mu.Unlock()
	_, err := tunnels.Create(&dto.CreateTunnelReq{Name: "failed path", EntryRuleID: secondEntry.ID, ExitRuleID: sharedExit.ID, AllowlistNodeIDs: []uint{}},
		testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelChainCreateFailed)
	if relay := exitRemote.service(left.ServiceID); relay.Name == "" || firstRemote.chain(left.ChainID).Name == "" {
		t.Fatal("rolling back a failed start removed the existing tunnel's Relay or chain")
	}
	if policy := exitRemote.policy(left.ServiceID); len(policy.Users) != 1 ||
		policy.Users[0].Username != firstRemote.chain(left.ChainID).Hops[0].Nodes[0].Connector.Auth.Username {
		t.Fatal("rolling back a failed start did not keep only the existing tunnel's Relay credentials")
	}
	secondRemote.mu.Lock()
	secondRemote.reject = nil
	secondRemote.mu.Unlock()
	right := startSharedTunnel(t, tunnels, "second path", secondEntry.ID, sharedExit.ID)
	left = loadSharedTunnel(t, tunnels, left.ID)
	if left.ServiceID == "" || left.ServiceID != right.ServiceID || left.ChainID == "" || right.ChainID == "" || left.ChainID == right.ChainID {
		t.Fatalf("expected one Relay and two independent chains, got %s %s and %s %s", left.ServiceID, left.ChainID, right.ServiceID, right.ChainID)
	}
	if relay := exitRemote.service(left.ServiceID); relay.Addr != ":18443" || relay.Handler == nil || relay.Handler.Type != "relay" {
		t.Fatalf("shared Relay = %+v", relay)
	}
	var users []string
	for _, link := range []struct {
		tunnel *model.GostTunnel
		remote *lifecycleNode
	}{{left, firstRemote}, {right, secondRemote}} {
		chain := link.remote.chain(link.tunnel.ChainID)
		addr, ok := chain.TunnelRelayAddress()
		if !ok || addr != "198.51.100.21:18443" {
			t.Fatalf("chain %s connects to %q", link.tunnel.ChainID, addr)
		}
		credentials := chain.Hops[0].Nodes[0].Connector.Auth
		policy := exitRemote.policy(left.ServiceID)
		if credentials == nil || !slices.ContainsFunc(policy.Users, func(user nodegrpc.RelayAuthUser) bool {
			return user.Username == credentials.Username && user.Password == credentials.Password
		}) {
			t.Fatalf("chain %s is missing its Relay authentication policy", link.tunnel.ChainID)
		}
		users = append(users, credentials.Username)
	}
	if users[0] == users[1] {
		t.Fatal("independent tunnels use the same Relay credentials")
	}

	requireServiceError(t, tunnels.Stop(left.ID, testAdmin, testIP, testAgent), nil)
	left, right = loadSharedTunnel(t, tunnels, left.ID), loadSharedTunnel(t, tunnels, right.ID)
	if left.Status != model.TunnelStatusStopped || left.EntryRule.Status != model.RuleStatusStopped ||
		right.Status != model.TunnelStatusRunning || right.EntryRule.Status != model.RuleStatusRunning ||
		right.ExitRule.Status != model.RuleStatusRunning {
		t.Fatalf("stopping one tunnel changed shared endpoint state: left %s/%s, right %s/%s/%s",
			left.Status, left.EntryRule.Status, right.Status, right.EntryRule.Status, right.ExitRule.Status)
	}
	if firstRemote.chain(left.ChainID).Name != "" || secondRemote.chain(right.ChainID).Name == "" ||
		exitRemote.service(right.ServiceID).Name == "" {
		t.Fatal("stopping one tunnel removed another tunnel's resources or retained its own chain")
	}
	if policy := exitRemote.policy(right.ServiceID); len(policy.Users) != 1 || policy.Users[0].Username != users[1] {
		t.Fatal("stopping one tunnel did not revoke only its Relay credentials")
	}

	requireServiceError(t, tunnels.Start(left.ID, testAdmin, testIP, testAgent), nil)
	left, right = loadSharedTunnel(t, tunnels, left.ID), loadSharedTunnel(t, tunnels, right.ID)
	if left.Status != model.TunnelStatusRunning || left.ServiceID != right.ServiceID ||
		right.Status != model.TunnelStatusRunning || secondRemote.chain(right.ChainID).Name == "" ||
		len(exitRemote.policy(right.ServiceID).Users) != 2 {
		t.Fatal("restarting the original tunnel failed to reuse the existing shared Relay")
	}
	requireServiceError(t, tunnels.Stop(left.ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, tunnels.Stop(right.ID, testAdmin, testIP, testAgent), nil)
	right = loadSharedTunnel(t, tunnels, right.ID)
	if right.ExitRule.Status != model.RuleStatusStopped || exitRemote.service(left.ServiceID).Name != "" ||
		len(exitRemote.policy(left.ServiceID).Users) != 0 {
		t.Fatal("stopping the last tunnel did not remove its shared Relay")
	}
}

func TestSharedExitRetainsItsDomainBypassUntilLastTunnelStops(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	firstRemote := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}, mark: 1234}
	secondRemote := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}, mark: 1234}
	exitRemote := &sharedRelayTestNode{lifecycleNode: lifecycleNode{t: t}, policies: make(map[string]nodegrpc.RelayAuthConfig)}
	first, second, exit := startServiceTestNode(t, firstRemote), startServiceTestNode(t, secondRemote), startServiceTestNode(t, exitRemote)
	first.Name, second.Name, exit.Name = "first entry", "second entry", "shared exit"
	exit.Address = "192.0.2.20"
	saveServiceFixtures(t, db, &first, &second, &exit)
	firstEntry := model.GostRule{Name: "first source", NodeID: &first.ID, Role: model.EndpointRoleEntry,
		ListenIP: "127.0.0.1", ListenPort: 18001, Protocol: model.RuleProtocolTransparent}
	secondEntry := model.GostRule{Name: "second source", NodeID: &second.ID, Role: model.EndpointRoleEntry,
		ListenIP: "127.0.0.1", ListenPort: 18002, Protocol: model.RuleProtocolTransparent}
	sharedExit := model.GostRule{Name: "filtered exit", NodeID: &exit.ID, Role: model.EndpointRoleExit,
		ListenPort: 18443, Protocol: model.RuleProtocolTCP, UseOriginalTarget: true, ManualDomains: []string{"match.test"}}
	saveServiceFixtures(t, db, &firstEntry, &secondEntry, &sharedExit)
	tunnels := NewTunnelService(db)

	firstPath := startSharedTunnel(t, tunnels, "first path", firstEntry.ID, sharedExit.ID)
	secondPath := startSharedTunnel(t, tunnels, "second path", secondEntry.ID, sharedExit.ID)
	relay := firstPath.ServiceID
	policy := relayPolicyName(relay)
	if relay == "" || secondPath.ServiceID != relay {
		t.Fatalf("shared exit used different Relay services: %q and %q", relay, secondPath.ServiceID)
	}
	assertExitResources := func(running bool) {
		t.Helper()
		exitRemote.mu.Lock()
		defer exitRemote.mu.Unlock()
		var services, bypasses []string
		for path := range exitRemote.resources {
			if name, ok := strings.CutPrefix(path, "/config/services/"); ok {
				services = append(services, name)
			}
			if name, ok := strings.CutPrefix(path, "/config/bypasses/"); ok {
				bypasses = append(bypasses, name)
			}
		}
		if !running {
			if len(services) != 0 || len(bypasses) != 0 {
				t.Fatalf("stopping the last tunnel left services %v and bypasses %v", services, bypasses)
			}
			return
		}
		if !slices.Equal(services, []string{relay}) || !slices.Equal(bypasses, []string{policy}) {
			t.Fatalf("expected one shared Relay and bypass, got services %v and bypasses %v", services, bypasses)
		}
		var service gost.ServiceConfig
		requireServiceError(t, json.Unmarshal(exitRemote.resources["/config/services/"+relay], &service), nil)
		var bypass struct {
			Whitelist bool
			Matchers  []string
		}
		requireServiceError(t, json.Unmarshal(exitRemote.resources["/config/bypasses/"+policy], &bypass), nil)
		if service.Bypass != policy || !bypass.Whitelist || !slices.Equal(bypass.Matchers, []string{".match.test:443"}) {
			t.Fatalf("shared Relay bypass = %q, policy = %+v", service.Bypass, bypass)
		}
	}
	assertExitResources(true)
	requireServiceError(t, tunnels.Stop(firstPath.ID, testAdmin, testIP, testAgent), nil)
	assertExitResources(true)
	requireServiceError(t, tunnels.Stop(secondPath.ID, testAdmin, testIP, testAgent), nil)
	assertExitResources(false)
}

type sharedRelayTestNode struct {
	lifecycleNode
	policies map[string]nodegrpc.RelayAuthConfig
}

func (n *sharedRelayTestNode) policy(service string) nodegrpc.RelayAuthConfig {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.policies[service]
}

func (n *sharedRelayTestNode) Execute(ctx context.Context, req *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	service, ok := strings.CutPrefix(req.GetPath(), nodegrpc.RelayAuthPathPrefix)
	if !ok {
		return n.lifecycleNode.Execute(ctx, req)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch req.GetMethod() {
	case http.MethodPut:
		var policy nodegrpc.RelayAuthConfig
		if err := json.Unmarshal(req.GetBody(), &policy); err != nil {
			return &nodegrpc.CommandReply{StatusCode: http.StatusBadRequest}, nil
		}
		n.policies[service] = policy
	case http.MethodDelete:
		delete(n.policies, service)
	case http.MethodGet:
		_, configured := n.policies[service]
		data, err := json.Marshal(nodegrpc.RelayAuthStatus{Configured: configured})
		return &nodegrpc.CommandReply{StatusCode: http.StatusOK, Body: data}, err
	default:
		return &nodegrpc.CommandReply{StatusCode: http.StatusMethodNotAllowed}, nil
	}
	return &nodegrpc.CommandReply{StatusCode: http.StatusOK}, nil
}

func TestEndpointTunnelsShareAnEntryListener(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, firstRemote, secondRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry := startServiceTestNode(t, entryRemote)
	first := startServiceTestNode(t, firstRemote)
	second := startServiceTestNode(t, secondRemote)
	entry.Name, first.Name, second.Name = "shared entry", "first exit", "second exit"
	first.Address, second.Address = "198.51.100.21", "198.51.100.22"
	saveServiceFixtures(t, db, &entry, &first, &second)
	sharedEntry := model.GostRule{Name: "shared source", NodeID: &entry.ID, Role: model.EndpointRoleEntry, Protocol: model.RuleProtocolTCP, ListenPort: 18001}
	firstExit := model.GostRule{Name: "first destination", NodeID: &first.ID, Role: model.EndpointRoleExit, Protocol: "tcp", ListenPort: 18443, Targets: []string{"one.test:443"}}
	secondExit := model.GostRule{Name: "second destination", NodeID: &second.ID, Role: model.EndpointRoleExit, Protocol: "tls", ListenPort: 18444, Targets: []string{"two.test:443"}}
	saveServiceFixtures(t, db, &sharedEntry, &firstExit, &secondExit)
	tunnels := NewTunnelService(db)

	left := startSharedTunnel(t, tunnels, "first path", sharedEntry.ID, firstExit.ID)
	right := startSharedTunnel(t, tunnels, "second path", sharedEntry.ID, secondExit.ID)
	left = loadSharedTunnel(t, tunnels, left.ID)
	listenerName := left.EntryRule.ServiceID
	leftRoute := sharedEntryRoute(t, entryRemote, left.ChainID)
	rightRoute := sharedEntryRoute(t, entryRemote, right.ChainID)
	if listenerName == "" || listenerName != right.EntryRule.ServiceID || leftRoute.Addr == rightRoute.Addr {
		t.Fatalf("expected one entry listener and independent route addresses, got %s %s %s", listenerName, leftRoute.Addr, rightRoute.Addr)
	}
	assertSharedEntryTargets(t, entryRemote, listenerName, leftRoute.Addr, rightRoute.Addr)
	if leftRoute.Forwarder == nil || len(leftRoute.Forwarder.Nodes) != 1 || leftRoute.Forwarder.Nodes[0].Addr != "one.test:443" ||
		rightRoute.Forwarder == nil || len(rightRoute.Forwarder.Nodes) != 1 || rightRoute.Forwarder.Nodes[0].Addr != "two.test:443" {
		t.Fatalf("entry route policy was not preserved: left=%+v right=%+v", leftRoute.Forwarder, rightRoute.Forwarder)
	}

	requireServiceError(t, tunnels.Stop(left.ID, testAdmin, testIP, testAgent), nil)
	right = loadSharedTunnel(t, tunnels, right.ID)
	if right.EntryRule.Status != model.RuleStatusRunning || right.Status != model.TunnelStatusRunning ||
		entryRemote.service(leftRoute.Name).Name != "" {
		t.Fatal("stopping one tunnel removed another tunnel's route or the shared listener")
	}
	listener := entryRemote.service(listenerName)
	if listener.Handler == nil {
		t.Fatal("stopping one tunnel left the entry listener without a handler")
	}
	if listener.Handler.Chain == right.ChainID {
		if listener.Forwarder == nil || len(listener.Forwarder.Nodes) != 1 || listener.Forwarder.Nodes[0].Addr != "two.test:443" {
			t.Fatal("entry listener lost the remaining tunnel's target after reverting to one route")
		}
	} else {
		if entryRemote.service(rightRoute.Name).Addr != rightRoute.Addr {
			t.Fatal("entry listener lost the remaining private forwarding service")
		}
		assertSharedEntryTargets(t, entryRemote, listenerName, rightRoute.Addr)
	}
	requireServiceError(t, tunnels.Stop(right.ID, testAdmin, testIP, testAgent), nil)
	if entryRemote.service(listenerName).Name != "" || entryRemote.service(rightRoute.Name).Name != "" {
		t.Fatal("stopping the last tunnel left the entry listener or route")
	}
}

func startSharedTunnel(t *testing.T, svc *TunnelService, name string, entry, exit uint) *model.GostTunnel {
	t.Helper()
	result, err := svc.Create(&dto.CreateTunnelReq{Name: name, EntryRuleID: entry, ExitRuleID: exit, AllowlistNodeIDs: []uint{}},
		testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	return result
}

func loadSharedTunnel(t *testing.T, svc *TunnelService, id uint) *model.GostTunnel {
	t.Helper()
	result, err := svc.GetByID(id)
	requireServiceError(t, err, nil)
	return result
}

func sharedEntryRoute(t *testing.T, node *lifecycleNode, chain string) gost.ServiceConfig {
	t.Helper()
	var names []string
	node.mu.Lock()
	for path := range node.resources {
		if name, ok := strings.CutPrefix(path, "/config/services/"); ok {
			names = append(names, name)
		}
	}
	node.mu.Unlock()
	for _, name := range names {
		svc := node.service(name)
		if svc.Handler != nil && svc.Handler.Chain == chain {
			host, _, err := net.SplitHostPort(svc.Addr)
			if err != nil || host != "127.0.0.1" {
				t.Fatalf("route for chain %s is not on loopback: %+v", chain, svc)
			}
			return svc
		}
	}
	t.Fatalf("route for chain %s not found", chain)
	return gost.ServiceConfig{}
}

func assertSharedEntryTargets(t *testing.T, node *lifecycleNode, name string, targets ...string) {
	t.Helper()
	listener := node.service(name)
	if listener.Addr != ":18001" || listener.Handler == nil || listener.Handler.Chain != "" ||
		listener.Forwarder == nil || len(listener.Forwarder.Nodes) != len(targets) {
		t.Fatalf("shared listener does not forward to its routes: %+v", listener)
	}
	addresses := make([]string, 0, len(targets))
	for _, route := range listener.Forwarder.Nodes {
		addresses = append(addresses, route.Addr)
	}
	slices.Sort(addresses)
	slices.Sort(targets)
	if !slices.Equal(addresses, targets) {
		t.Fatalf("local targets %v, want %v", addresses, targets)
	}
}
