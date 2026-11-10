package service

import (
	"fmt"
	"net/http"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"
)

func TestEndpointTunnelAppliesAndRemovesBothNodeConfigs(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	entry.IPv4 = "192.0.2.13"
	exit.Name, exit.Address = "exit", "198.51.100.21"
	saveServiceFixtures(t, db, &entry, &exit)
	rules, tunnels := NewRuleService(db), NewTunnelService(db)
	create := func(node uint, name, listenIP string, port int, targets ...string) *model.GostRule {
		t.Helper()
		r, err := rules.Create(&dto.CreateRuleReq{NodeID: &node, Name: name, Protocol: "tcp", ListenIP: listenIP, ListenPort: port, Targets: targets}, testAdmin, testIP, testAgent)
		requireServiceError(t, err, nil)
		return r
	}
	source := create(entry.ID, "source", "192.0.2.13", 8915, "origin.test:8090")
	destination := create(exit.ID, "destination", "", 9816)
	loopback := create(exit.ID, "loopback destination", "127.0.0.1", 8917)
	local := create(entry.ID, "same node", "", 8918)
	if entryRemote.service("rule-1").Name != "" || exitRemote.service("rule-2").Name != "" {
		t.Fatal("saving an entrance unexpectedly configured a node")
	}
	for _, scenario := range []struct {
		source, destination uint
		err                 error
	}{
		{source.ID, source.ID, bizerrors.ErrTunnelEndpointsRequired},
		{destination.ID, source.ID, bizerrors.ErrTunnelEntryInvalid},
		{source.ID, loopback.ID, bizerrors.ErrTunnelExitInvalid},
		{source.ID, local.ID, bizerrors.ErrTunnelNodeSame},
	} {
		_, err := tunnels.Create(&dto.CreateTunnelReq{Name: "invalid", EntryRuleID: scenario.source, ExitRuleID: scenario.destination}, testAdmin, testIP, testAgent)
		requireServiceError(t, err, scenario.err)
	}
	request := &dto.CreateTunnelReq{Name: "site tunnel", EntryRuleID: source.ID, ExitRuleID: destination.ID}
	mixed := *request
	mixed.EntryNodeID = entry.ID
	_, err := tunnels.Create(&mixed, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelEndpointsRequired)
	for _, tc := range []struct {
		nodeID uint
		want   error
	}{
		{entry.ID, bizerrors.ErrEntryNodeOffline},
		{exit.ID, bizerrors.ErrExitNodeOffline},
	} {
		requireServiceError(t, db.Model(&model.GostNode{}).Where("id = ?", tc.nodeID).Update("status", model.NodeStatusOffline).Error, nil)
		_, err := tunnels.Create(request, testAdmin, testIP, testAgent)
		requireServiceError(t, err, tc.want)
		requireServiceError(t, db.Model(&model.GostNode{}).Where("id = ?", tc.nodeID).Update("status", model.NodeStatusOnline).Error, nil)
	}
	requireServiceError(t, db.Model(&exit).Update("address", "").Error, nil)
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrExtractHostFailed)
	requireServiceError(t, db.Model(&exit).Update("address", "198.51.100.21").Error, nil)
	exitRemote.mu.Lock()
	exitRemote.reject = map[string]int{"POST /config/services": http.StatusConflict}
	exitRemote.mu.Unlock()
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelRelayCreateFailed)
	exitRemote.mu.Lock()
	delete(exitRemote.reject, "POST /config/services")
	exitRemote.mu.Unlock()
	entryRemote.mu.Lock()
	entryRemote.reject = map[string]int{"POST /config/chains": http.StatusBadGateway}
	entryRemote.mu.Unlock()
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelChainCreateFailed)
	if len(exitRemote.resources) != 0 {
		t.Fatalf("failed tunnel leaked resources: %v", exitRemote.resources)
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "POST /config/chains")
	entryRemote.mu.Unlock()
	request.AllowlistNodeIDs = []uint{entry.ID}
	tunnel, err := tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if tunnel.Status != model.TunnelStatusRunning || tunnel.EntryRule == nil || tunnel.ExitRule == nil ||
		tunnel.EntryRule.ID != source.ID || tunnel.ExitRule.ID != destination.ID || tunnel.RelayPort != destination.ListenPort {
		t.Fatalf("linked tunnel = %+v", tunnel)
	}
	forward := entryRemote.service(tunnel.EntryRule.ServiceID)
	relay := exitRemote.service(tunnel.ServiceID)
	chain := entryRemote.chain(tunnel.ChainID)
	if forward.Addr != "192.0.2.13:8915" || forward.Handler.Chain != chain.Name || forward.Forwarder.Nodes[0].Addr != "origin.test:8090" {
		t.Fatalf("source configuration = %+v", forward)
	}
	if relay.Addr != ":9816" || relay.Metadata["enableStats"] != true || relay.Handler.Auth == nil || relay.Handler.Auth.Password == "" ||
		*chain.Hops[0].Nodes[0].Connector.Auth != *relay.Handler.Auth || chain.Hops[0].Nodes[0].Addr != "198.51.100.21:9816" {
		t.Fatalf("exit and chain do not use the same protected Relay endpoint")
	}
	allowlist := exitRemote.admission(gost.TunnelRelayAllowlistName(relay.Name))
	if relay.Admission != allowlist.Name || !allowlist.Whitelist || len(allowlist.Matchers) != 1 || allowlist.Matchers[0] != entry.IPv4 {
		t.Fatalf("endpoint Relay admission = %q, %+v", relay.Admission, allowlist)
	}
	_, err = tunnels.Create(request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelEndpointInUse)
	requireServiceError(t, rules.Stop(source.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleTunnelLinked)
	sync := NewRuleSyncService(db)
	client := utils.GetGostClient(&entry)
	exitAddresses := map[uint]string{exit.ID: "198.51.100.22"}
	chainPath := "PUT /config/chains/" + chain.Name
	entryRemote.mu.Lock()
	entryRemote.reject[chainPath] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	sync.syncTunnels(client, entry.ID, []model.GostTunnel{*tunnel}, []gost.ChainConfig{chain}, exitAddresses)
	if addr, _ := entryRemote.chain(chain.Name).TunnelRelayAddress(); addr != "198.51.100.21:9816" {
		t.Fatalf("failed update did not retain the previous relay address: %q", addr)
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, chainPath)
	entryRemote.reject["POST /config?format=yaml"] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	sync.syncTunnels(client, entry.ID, []model.GostTunnel{*tunnel}, []gost.ChainConfig{chain}, exitAddresses)
	if addr, _ := entryRemote.chain(chain.Name).TunnelRelayAddress(); addr != "198.51.100.22:9816" {
		t.Fatalf("updated relay address = %q", addr)
	}
	if _, pending := sync.unsavedTunnelConfig.Load(entry.ID); !pending {
		t.Fatal("failed save was not scheduled for retry")
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "POST /config?format=yaml")
	entryRemote.mu.Unlock()
	sync.syncTunnels(client, entry.ID, []model.GostTunnel{*tunnel}, []gost.ChainConfig{entryRemote.chain(chain.Name)}, exitAddresses)
	if _, pending := sync.unsavedTunnelConfig.Load(entry.ID); pending {
		t.Fatal("successful retry did not clear pending save")
	}
	requireServiceError(t, repository.NewTunnelRepository(db).StopByNodeID(exit.ID), nil)
	stillApplied, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if stillApplied.Status != model.TunnelStatusRunning {
		t.Fatalf("node control disconnect changed applied tunnel status to %s", stillApplied.Status)
	}
	requireServiceError(t, db.Model(&exit).Update("status", model.NodeStatusOffline).Error, nil)
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), bizerrors.ErrExitNodeOffline)
	requireServiceError(t, db.Model(&exit).Update("status", model.NodeStatusOnline).Error, nil)
	entryRemote.mu.Lock()
	entryRemote.reject["DELETE /config/services/"+forward.Name] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	if err := tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent); err == nil || entryRemote.chain(chain.Name).Name == "" {
		t.Fatal("failed source stop should leave its chain in place")
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "DELETE /config/services/"+forward.Name)
	entryRemote.reject["DELETE /config/chains/"+chain.Name] = http.StatusBadGateway
	entryRemote.mu.Unlock()
	if err := tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent); err == nil || entryRemote.service(forward.Name).Name != "" {
		t.Fatal("failed chain cleanup must report failure and leave the source stopped")
	}
	entryRemote.mu.Lock()
	delete(entryRemote.reject, "DELETE /config/chains/"+chain.Name)
	entryRemote.mu.Unlock()
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if entryRemote.service(forward.Name).Name != "" || entryRemote.chain(chain.Name).Name != "" || exitRemote.service(relay.Name).Name != "" {
		t.Fatal("stopping a tunnel did not remove both node configs")
	}
	newDestination := create(exit.ID, "stable local IP", "10.11.12.13", 10789)
	updated, err := tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "replacement destination", EntryRuleID: source.ID, ExitRuleID: newDestination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if updated.RelayPort != 10789 || updated.Status != model.TunnelStatusStopped {
		t.Fatalf("updated stopped tunnel = %+v", updated)
	}
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if got := exitRemote.service(relay.Name); got.Addr != "10.11.12.13:10789" {
		t.Fatalf("new exit bind = %q", got.Addr)
	}
	requireServiceError(t, tunnels.Delete(tunnel.ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, rules.Delete(source.ID, testAdmin, testIP, testAgent), nil)
}

func TestEndpointTunnelRetainsLinkForRetryIfNodeCannotSave(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote := &lifecycleNode{t: t}
	exitRemote := &lifecycleNode{t: t, reject: map[string]int{"POST /config?format=yaml": http.StatusBadGateway}}
	entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
	exit.Name = "exit"
	saveServiceFixtures(t, db, &entry, &exit)
	entryRule := model.GostRule{Name: "source", NodeID: &entry.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP, ListenPort: 10110, Targets: []string{"origin.test:1234"}}
	exitRule := model.GostRule{Name: "exit", NodeID: &exit.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP, ListenPort: 10111}
	saveServiceFixtures(t, db, &entryRule, &exitRule)
	svc := NewTunnelService(db)
	_, err := svc.Create(&dto.CreateTunnelReq{Name: "retry", EntryRuleID: entryRule.ID, ExitRuleID: exitRule.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelEndpointSave)
	rows, _, err := svc.List(&dto.TunnelListReq{})
	requireServiceError(t, err, nil)
	if len(rows) != 1 || rows[0].Status != model.TunnelStatusError {
		t.Fatalf("failed save should retain one retryable link, got %v", rows)
	}
	if entryRemote.service(fmt.Sprintf("rule-%d", entryRule.ID)).Name != "" || exitRemote.service(relayServiceName(rows[0].ID)).Name != "" {
		t.Fatal("failed save should remove the live service on both endpoints")
	}
	_, err = svc.Update(rows[0].ID, &dto.UpdateTunnelReq{Name: "retry", EntryRuleID: entryRule.ID, ExitRuleID: exitRule.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelEndpointSave)
	exitRemote.mu.Lock()
	delete(exitRemote.reject, "POST /config?format=yaml")
	exitRemote.mu.Unlock()
	_, err = svc.Update(rows[0].ID, &dto.UpdateTunnelReq{Name: "retry", EntryRuleID: entryRule.ID, ExitRuleID: exitRule.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, svc.Start(rows[0].ID, testAdmin, testIP, testAgent), nil)
	requireServiceError(t, svc.Delete(rows[0].ID, testAdmin, testIP, testAgent), nil)
}

func TestEndpointTunnelRetriesEntranceCleanupAfterFailedFirstSave(t *testing.T) {
	for _, action := range []string{"stop", "delete"} {
		t.Run(action, func(t *testing.T) {
			db := newServiceTestDB(t)
			loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
			entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
			entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
			exit.Name = "exit"
			saveServiceFixtures(t, db, &entry, &exit)
			entryRule := model.GostRule{Name: "source", NodeID: &entry.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP, ListenPort: 10110, Targets: []string{"origin.test:1234"}}
			exitRule := model.GostRule{Name: "exit", NodeID: &exit.ID, Type: model.RuleTypeForward, Protocol: model.RuleProtocolTCP, ListenPort: 10111}
			saveServiceFixtures(t, db, &entryRule, &exitRule)
			serviceID := fmt.Sprintf("rule-%d", entryRule.ID)
			entryRemote.mu.Lock()
			entryRemote.reject = map[string]int{
				"POST /config?format=yaml":             http.StatusBadGateway,
				"DELETE /config/services/" + serviceID: http.StatusBadGateway,
			}
			entryRemote.mu.Unlock()
			svc := NewTunnelService(db)
			_, err := svc.Create(&dto.CreateTunnelReq{Name: "retry", EntryRuleID: entryRule.ID, ExitRuleID: exitRule.ID}, testAdmin, testIP, testAgent)
			requireServiceError(t, err, bizerrors.ErrRuleStartFailed)

			// Reload through a new service, as a later request or panel restart would.
			svc = NewTunnelService(db)
			rows, _, err := svc.List(&dto.TunnelListReq{})
			requireServiceError(t, err, nil)
			if len(rows) != 1 || rows[0].Status != model.TunnelStatusError {
				t.Fatalf("failed rollback should retain one retryable tunnel: %+v", rows)
			}
			tunnel, err := svc.GetByID(rows[0].ID)
			requireServiceError(t, err, nil)
			if tunnel.EntryRule.ServiceID != serviceID || tunnel.EntryRule.Status != model.RuleStatusError || entryRemote.service(serviceID).Name == "" {
				t.Fatalf("live entrance identity was lost: %+v", tunnel.EntryRule)
			}
			if entryRemote.chain(tunnelChainName(tunnel.ID)).Name == "" || exitRemote.service(relayServiceName(tunnel.ID)).Name == "" {
				t.Fatal("failed entrance rollback must retain its chain and relay")
			}
			entryRemote.mu.Lock()
			entryRemote.reject = nil
			entryRemote.mu.Unlock()
			if action == "stop" {
				requireServiceError(t, svc.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
			} else {
				requireServiceError(t, svc.Delete(tunnel.ID, testAdmin, testIP, testAgent), nil)
			}
			if entryRemote.service(serviceID).Name != "" || entryRemote.chain(tunnelChainName(tunnel.ID)).Name != "" || exitRemote.service(relayServiceName(tunnel.ID)).Name != "" {
				t.Fatal("retry left a live entrance, chain or relay behind")
			}
		})
	}
}
