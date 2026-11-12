package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSelectedClashEntranceGetsSubscriptionWithoutManualNodeSelection(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}, dockerAddress: "172.17.0.1", mark: 1234}
	exitRemote := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name, exit.Address = "exit", "198.51.100.21"
	saveServiceFixtures(t, db, &entry, &exit)
	a := model.GostRule{Name: "Clash source", NodeID: &entry.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP,
		ListenIP: "127.0.0.1", ListenPort: 10951}
	b := model.GostRule{Name: "relay", NodeID: &exit.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP, ListenPort: 10952}
	saveServiceFixtures(t, db, &a, &b)
	request := &dto.CreateTunnelReq{Name: "transparent", EntryRuleID: a.ID, ExitRuleID: b.ID, UseClashRuleset: true, Strategy: "round"}
	tunnels := NewTunnelService(db)
	_, err := tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleClashUnavailable)
	ruleset := model.GostRuleset{ID: gostRulesetID, URL: "https://example.test/rules", Matchers: []string{".old.test"}}
	saveServiceFixtures(t, db, &ruleset)
	entryRemote.mu.Lock()
	entryRemote.reject = map[string]int{"POST /config/bypasses": http.StatusBadGateway}
	entryRemote.mu.Unlock()
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleChainCreateFailed)
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "POST /config/bypasses")
	entryRemote.mu.Unlock()
	if got, _ := entryRemote.savedService(); got != 0 {
		t.Fatal("failed tunnel provisioning persisted Gost config or enabled redirection")
	}
	entryRemote.mu.Lock()
	entryRemote.prepareCode = codes.FailedPrecondition
	entryRemote.mu.Unlock()
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelTransparentRedirect)
	entrySaves, _ := entryRemote.savedService()
	exitSaves, _ := exitRemote.savedService()
	if entrySaves != 0 || exitSaves != 0 {
		t.Fatal("mark conflict persisted Gost configuration")
	}
	entryRemote.mu.Lock()
	entryRemote.prepareCode, entryRemote.rejectApply = codes.OK, true
	entryRemote.mu.Unlock()
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelTransparentRedirect)
	entrySaves, entrySavedService := entryRemote.savedService()
	exitSaves, exitSavedService := exitRemote.savedService()
	active, _, _ := entryRemote.redirectStatus()
	if active || entrySaves == 0 || exitSaves == 0 || entrySavedService || exitSavedService ||
		entryRemote.service(fmt.Sprintf("rule-%d", a.ID)).Name != "" ||
		entryRemote.service(dockerEntranceServiceName(a.ID)).Name != "" {
		t.Fatal("failed redirect installation did not roll back both saved listeners")
	}
	entryRemote.mu.Lock()
	entryRemote.rejectApply = false
	entryRemote.mu.Unlock()
	tunnel, err := tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if !tunnel.UseClashRuleset || tunnel.EntryRule.UseClashRuleset {
		t.Fatal("Clash target configuration must belong to the tunnel, not its source endpoint")
	}
	forward := entryRemote.service(tunnel.EntryRule.ServiceID)
	clash := entryRemote.chain(forward.Handler.Chain)
	relay := exitRemote.service(tunnel.ServiceID)
	if forward.Addr != "127.0.0.1:10951" || relay.Addr != ":10952" || forward.Handler.Chain != clashTunnelChainName(tunnel.ID) ||
		*clash.Hops[0].Nodes[0].Connector.Auth != *relay.Handler.Auth {
		t.Fatal("selected Clash source and destination do not use the same authenticated Relay")
	}
	docker := entryRemote.service(dockerEntranceServiceName(a.ID))
	if docker.Addr != "172.17.0.1:10951" || docker.Handler.Chain != forward.Handler.Chain ||
		docker.SockOpts.Mark != int(entryRemote.mark) || forward.SockOpts.Mark != int(entryRemote.mark) ||
		docker.Listener.Metadata["tproxy"] != true || docker.Handler.Metadata["tproxy"] != nil {
		t.Fatalf("Docker and loopback listeners do not share the selected mark and chain: %+v, %+v", docker, forward)
	}
	var marked struct {
		Hops []struct {
			SockOpts gost.SocketOptions
			Nodes    []struct{ SockOpts gost.SocketOptions }
		}
	}
	entryRemote.read("/config/chains/"+forward.Handler.Chain, &marked)
	if marked.Hops[0].SockOpts.Mark != int(entryRemote.mark) || marked.Hops[0].Nodes[0].SockOpts.Mark != int(entryRemote.mark) {
		t.Fatalf("relay chain did not use the selected mark: %+v", marked)
	}
	active, savedOnApply, lastRelayHost := entryRemote.redirectStatus()
	_, exitSavedService = exitRemote.savedService()
	if !active || !savedOnApply || lastRelayHost != exit.Address || !exitSavedService {
		t.Fatal("redirect applied before establishing and saving the tunnel")
	}
	ruleset.Matchers = []string{".new.test"}
	requireServiceError(t, db.Save(&ruleset).Error, nil)
	subscriptions := NewGostRulesetService(db)
	unsaved := make(map[uint]bool)
	bypassPath := "PUT /config/bypasses/" + gost.TransparentBypassName
	entryRemote.mu.Lock()
	entryRemote.reject[bypassPath] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	subscriptions.sync(t.Context(), unsaved)
	var matchers gost.AdmissionConfig
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !reflect.DeepEqual(matchers.Matchers, []string{".old.test"}) || unsaved[entry.ID] {
		t.Fatalf("failed update changed the active subscription: %+v, %v", matchers, unsaved)
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, bypassPath)
	entryRemote.reject["POST /config?format=yaml"] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	subscriptions.sync(t.Context(), unsaved)
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !reflect.DeepEqual(matchers.Matchers, ruleset.Matchers) || !unsaved[entry.ID] {
		t.Fatalf("subscription reached the selected entry but must be saved again: %+v, %v", matchers, unsaved)
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "POST /config?format=yaml")
	entryRemote.mu.Unlock()
	subscriptions.sync(t.Context(), unsaved)
	if unsaved[entry.ID] {
		t.Fatal("retry did not save the changed subscription")
	}
	nextHost := "198.51.100.22"
	base := entryRemote.chain(tunnel.ChainID)
	NewRuleSyncService(db).syncTunnels(utils.GetGostClient(&entry), entry.ID, []model.GostTunnel{*tunnel},
		[]gost.ChainConfig{base, clash}, map[uint]*model.GostNode{exit.ID: {Address: nextHost}})
	active, _, lastRelayHost = entryRemote.redirectStatus()
	address, _ := entryRemote.chain(clash.Name).TunnelRelayAddress()
	if !active || lastRelayHost != nextHost || address != nextHost+":10952" {
		t.Fatalf("changed relay host did not reach nftables and Gost: active=%v nft=%q Gost=%q", active, lastRelayHost, address)
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	active, _, _ = entryRemote.redirectStatus()
	if active || entryRemote.service(forward.Name).Name != "" || entryRemote.service(docker.Name).Name != "" {
		t.Fatal("stopping the tunnel left an active redirect or listener")
	}
}

type redirectLifecycleNode struct {
	lifecycleNode
	mark           uint32
	dockerAddress  string
	lastRelayHost  string
	saves          []bool
	savedOnApply   bool
	redirectActive bool
	prepareCode    codes.Code
	rejectApply    bool
	removeCode     codes.Code
	rejectResource string
}

func (n *redirectLifecycleNode) Execute(ctx context.Context, request *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if request.GetMethod() == http.MethodPost && (request.GetPath() == "/config/services" || request.GetPath() == "/config/chains") {
		var resource struct{ Name string }
		n.mu.Lock()
		rejected := n.rejectResource != "" && json.Unmarshal(request.GetBody(), &resource) == nil && resource.Name == n.rejectResource
		n.mu.Unlock()
		if rejected {
			return &nodegrpc.CommandReply{StatusCode: http.StatusServiceUnavailable}, nil
		}
	}
	reply, err := n.lifecycleNode.Execute(ctx, request)
	if request.GetMethod() == http.MethodPost && request.GetPath() == "/config?format=yaml" && reply.GetStatusCode() == http.StatusOK {
		n.mu.Lock()
		defer n.mu.Unlock()
		hasService := false
		for name := range n.resources {
			if strings.HasPrefix(name, "/config/services/") {
				hasService = true
				break
			}
		}
		n.saves = append(n.saves, hasService)
	}
	return reply, err
}

func (n *redirectLifecycleNode) ConfigureTransparentRedirect(_ context.Context, request *nodegrpc.TransparentRedirectRequest) (*nodegrpc.TransparentRedirectReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch request.GetAction() {
	case nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_PREPARE:
		if n.prepareCode != codes.OK {
			return nil, status.Error(n.prepareCode, "cannot prepare redirect")
		}
		return &nodegrpc.TransparentRedirectReply{Mark: n.mark, DockerAddress: n.dockerAddress, Active: n.redirectActive}, nil
	case nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY:
		n.savedOnApply = len(n.saves) > 0 && n.saves[len(n.saves)-1]
		if n.rejectApply {
			return nil, status.Error(codes.FailedPrecondition, "cannot apply redirect")
		}
		n.lastRelayHost = request.GetRelayHost()
		n.redirectActive = true
	case nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_REMOVE:
		if n.removeCode != codes.OK {
			return nil, status.Error(n.removeCode, "cannot remove redirect")
		}
		n.redirectActive = false
	default:
		return nil, status.Error(codes.InvalidArgument, "unexpected redirect action")
	}
	return &nodegrpc.TransparentRedirectReply{Active: n.redirectActive}, nil
}

func (n *redirectLifecycleNode) savedService() (int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.saves), len(n.saves) > 0 && n.saves[len(n.saves)-1]
}

func (n *redirectLifecycleNode) redirectStatus() (active, savedOnApply bool, relayHost string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.redirectActive, n.savedOnApply, n.lastRelayHost
}

func TestClashEntranceUsesSeparateChainAndFollowsExit(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry := startServiceTestNode(t, entryRemote)
	exit := startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	tunnels, rules := NewTunnelService(db), NewRuleService(db)
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{
		Name: "route A to B", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 8443,
	}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	tunnel, err = tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)

	request := dto.CreateRuleReq{
		Name: "local transparent TCP", TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "tcp",
		ListenIP: "127.0.0.1", ListenPort: 443, UseClashRuleset: true,
	}
	for _, invalid := range []dto.CreateRuleReq{
		{TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: 443, UseClashRuleset: true},
		{TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "tcp", ListenIP: "192.0.2.3", ListenPort: 443, UseClashRuleset: true},
		{TunnelID: &tunnel.ID, Type: "tunnel", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: 443, UseClashRuleset: true, Targets: []string{"fixed.test:443"}},
	} {
		_, err = rules.Create(&invalid, testAdmin, testIP, testAgent)
		requireServiceError(t, err, bizerrors.ErrRuleClashInvalid)
	}
	rule, err := rules.Create(&request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleClashUnavailable)
	ruleset := &model.GostRuleset{ID: gostRulesetID, URL: "https://example.test/list", Matchers: []string{".first.test"}}
	saveServiceFixtures(t, db, ruleset)
	requireServiceError(t, rules.Start(rule.ID, testAdmin, testIP, testAgent), nil)
	rule, err = rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	service := entryRemote.service(rule.ServiceID)
	if rule.Status != model.RuleStatusRunning || service.Listener == nil || service.Handler == nil ||
		service.Addr != "127.0.0.1:443" || service.Listener.Type != "red" || service.Handler.Type != "red" ||
		service.Handler.Chain != clashTunnelChainName(tunnel.ID) || service.Forwarder != nil ||
		service.SockOpts == nil || service.SockOpts.Mark != gost.ClashRedirectMark || service.Handler.Metadata["sniffing"] != true {
		t.Fatalf("transparent rule and Gost service: %+v, %+v", rule, service)
	}
	var matchers gost.AdmissionConfig
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !matchers.Whitelist || !reflect.DeepEqual(matchers.Matchers, ruleset.Matchers) {
		t.Fatalf("initial Gost matchers: %+v", matchers)
	}
	// The next subscription update reaches the rule's entry without a manually selected node.
	ruleset.Matchers = []string{"192.0.2.0/24"}
	requireServiceError(t, db.Save(ruleset).Error, nil)
	NewGostRulesetService(db).sync(t.Context(), make(map[uint]bool))
	entryRemote.read("/config/bypasses/"+gost.TransparentBypassName, &matchers)
	if !reflect.DeepEqual(matchers.Matchers, ruleset.Matchers) {
		t.Fatalf("updated Gost matchers: %+v", matchers)
	}
	base := entryRemote.chain(tunnel.ChainID)
	clash := entryRemote.chain(service.Handler.Chain)
	NewRuleSyncService(db).syncTunnels(utils.GetGostClient(&entry), entry.ID, []model.GostTunnel{*tunnel},
		[]gost.ChainConfig{base, clash}, map[uint]*model.GostNode{exit.ID: {Address: "198.51.100.20"}})
	for _, name := range []string{base.Name, clash.Name} {
		addr, found := entryRemote.chain(name).TunnelRelayAddress()
		if !found || addr != "198.51.100.20:8443" {
			t.Errorf("updated route %q = %q, %t", name, addr, found)
		}
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if entryRemote.chain(service.Handler.Chain).Name != "" {
		t.Fatal("stopping tunnel did not remove its Clash chain")
	}
}
