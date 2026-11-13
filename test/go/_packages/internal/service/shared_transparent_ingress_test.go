package service

import (
	"slices"
	"strings"
	"testing"

	"yagp/internal/model"
)

func TestSharedTransparentIngressUsesRulesBeforeFallback(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	ingress := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}, mark: 1234, dockerAddress: "172.17.0.1", multiRelay: true}
	entry := startServiceTestNode(t, ingress)
	matched := startServiceTestNode(t, &lifecycleNode{t: t})
	unmatched := startServiceTestNode(t, &lifecycleNode{t: t})
	matched.Address, unmatched.Address = "192.0.2.20", "192.0.2.30"
	saveServiceFixtures(t, db, &entry, &matched, &unmatched)
	entryRule := model.GostRule{Name: "transparent source", NodeID: &entry.ID, Role: model.EndpointRoleEntry,
		ListenIP: "127.0.0.1", ListenPort: 18001, Protocol: model.RuleProtocolTransparent}
	matchedExit := model.GostRule{Name: "matched exit", NodeID: &matched.ID, Role: model.EndpointRoleExit,
		ListenPort: 18443, Protocol: model.RuleProtocolTCP, UseOriginalTarget: true, ManualDomains: []string{"match.test"}}
	fallbackExit := model.GostRule{Name: "fallback exit", NodeID: &unmatched.ID, Role: model.EndpointRoleExit,
		ListenPort: 18443, Protocol: model.RuleProtocolTCP, UseOriginalTarget: true}
	saveServiceFixtures(t, db, &entryRule, &matchedExit, &fallbackExit)
	tunnels := NewTunnelService(db)
	first := startSharedTunnel(t, tunnels, "matched path", entryRule.ID, matchedExit.ID)
	second := startSharedTunnel(t, tunnels, "fallback path", entryRule.ID, fallbackExit.ID)

	for _, name := range []string{entranceServiceName(entryRule.ID), dockerEntranceServiceName(entryRule.ID)} {
		svc := ingress.service(name)
		if svc.Handler == nil || svc.Handler.ChainGroup == nil || len(svc.Handler.ChainGroup.Chains) != 2 {
			t.Fatalf("shared service %s missing chain group: %+v", name, svc.Handler)
		}
		routes := svc.Handler.ChainGroup.Chains
		if routes[0].Chain != sharedTransparentChainName(first.ID) || routes[1].Chain != sharedTransparentChainName(second.ID) ||
			routes[0].Matcher == nil || routes[1].Matcher == nil || !strings.Contains(routes[0].Matcher.Rule, transparentPolicyName(first.ID)) ||
			routes[1].Matcher.Rule != "!("+routes[0].Matcher.Rule+")" {
			t.Fatalf("unexpected chain routing: %+v", routes)
		}
	}
	if hosts := ingress.lastRelayHosts; !slices.Contains(hosts, matched.Address) || !slices.Contains(hosts, unmatched.Address) {
		t.Fatalf("redirect did not include both relay hosts: %v", hosts)
	}
	requireServiceError(t, tunnels.Stop(second.ID, testAdmin, testIP, testAgent), nil)
	if hosts := ingress.lastRelayHosts; !slices.Equal(hosts, []string{matched.Address}) {
		t.Fatalf("stopping one tunnel did not keep the survivor's Relay host: %v", hosts)
	}
	routes := ingress.service(entranceServiceName(entryRule.ID)).Handler.ChainGroup.Chains
	if len(routes) != 1 || routes[0].Chain != sharedTransparentChainName(first.ID) || ingress.chain(sharedTransparentChainName(second.ID)).Name != "" {
		t.Fatalf("stopping one tunnel left incorrect shared routes: %+v", routes)
	}
}

func TestSharedTransparentIngressOnlyRoutesMatchingTargetsToFilteredExits(t *testing.T) {
	var tunnels []*model.GostTunnel
	for i := uint(1); i <= 2; i++ {
		tunnels = append(tunnels, &model.GostTunnel{ID: i, EntryRule: &model.GostRule{Protocol: model.RuleProtocolTransparent},
			ExitRule: &model.GostRule{Role: model.EndpointRoleExit, UseOriginalTarget: true, ManualDomains: []string{"example.test"}}})
	}
	group, sniff := sharedTransparentGroup(tunnels)
	if !sniff || group.Selector.Strategy != "round" || len(group.Chains) != 2 {
		t.Fatalf("invalid shared routing group: %+v", group)
	}
	for i, route := range group.Chains {
		if route.Matcher == nil || !strings.Contains(route.Matcher.Rule, transparentPolicyName(tunnels[i].ID)) {
			t.Errorf("route is missing its whitelist: %+v", route)
		}
	}
}
