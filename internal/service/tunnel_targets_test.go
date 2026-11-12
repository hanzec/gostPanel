package service

import (
	"slices"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/pkg/gost"
)

func TestTunnelOwnsTargetsAndCanReverseEndpointRoles(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	firstRemote, secondRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
	first, second := startServiceTestNode(t, firstRemote), startServiceTestNode(t, secondRemote)
	second.Name = "second"
	saveServiceFixtures(t, db, &first, &second)
	source := model.GostRule{Name: "unassigned", NodeID: &first.ID, ListenPort: 18001}
	destination := model.GostRule{Name: "old forward", NodeID: &second.ID, ListenPort: 18002,
		Targets: []string{"legacy.test:443"}, Strategy: "hash"}
	saveServiceFixtures(t, db, &source, &destination)
	tunnels := NewTunnelService(db)
	targets := []string{"one.test:443", "two.test:443"}
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{Name: "new targets", EntryRuleID: source.ID, ExitRuleID: destination.ID,
		Targets: targets, Strategy: "fifo"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if !slices.Equal(tunnel.Targets, targets) || tunnel.Strategy != "fifo" || tunnel.UseClashRuleset {
		t.Fatalf("tunnel did not persist its target settings: %+v", tunnel)
	}
	forward := firstRemote.service(entranceServiceName(source.ID))
	if len(forward.Forwarder.Nodes) != 2 || forward.Forwarder.Nodes[0].Addr != targets[0] ||
		forward.Forwarder.Nodes[1].Addr != targets[1] || forward.Forwarder.Selector.Strategy != "fifo" {
		t.Fatalf("forwarder did not use tunnel settings: %+v", forward.Forwarder)
	}
	rules := repository.NewRuleRepository(db)
	storedSource, err := rules.FindByID(source.ID)
	requireServiceError(t, err, nil)
	storedDestination, err := rules.FindByID(destination.ID)
	requireServiceError(t, err, nil)
	if len(storedSource.Targets) != 0 || !slices.Equal(storedDestination.Targets, destination.Targets) || storedDestination.Strategy != "hash" {
		t.Fatal("tunnel configuration changed reusable endpoint settings")
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	// A request from an older client must retain targets already owned by the tunnel.
	_, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "renamed", EntryRuleID: source.ID, ExitRuleID: destination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if !slices.Equal(stored.Targets, targets) || stored.Strategy != "fifo" {
		t.Fatalf("omitted settings cleared tunnel targets: %+v", stored)
	}
	_, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "reversed", EntryRuleID: destination.ID, ExitRuleID: source.ID,
		Targets: []string{"new-one.test:8443", "new-two.test:8443"}, Strategy: "rand"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	forward = secondRemote.service(entranceServiceName(destination.ID))
	if forward.Forwarder.Nodes[0].Addr != "new-one.test:8443" || forward.Forwarder.Selector.Strategy != "rand" {
		t.Fatalf("restarted reverse tunnel did not use updated targets: %+v", forward.Forwarder)
	}
	if firstRemote.service(relayServiceName(tunnel.ID)).Addr != ":18001" {
		t.Fatal("previous source endpoint could not serve as the destination")
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
}

func TestTunnelTargetValidationUsesSelectedSource(t *testing.T) {
	db := newServiceTestDB(t)
	first, second := model.GostNode{Name: "first"}, model.GostNode{Name: "second"}
	saveServiceFixtures(t, db, &first, &second)
	source := model.GostRule{Name: "source", NodeID: &first.ID, ListenPort: 18001}
	destination := model.GostRule{Name: "destination", NodeID: &second.ID, ListenPort: 18002}
	saveServiceFixtures(t, db, &source, &destination)
	tunnels := NewTunnelService(db)
	_, err := tunnels.Create(&dto.CreateTunnelReq{Name: "missing targets", EntryRuleID: source.ID, ExitRuleID: destination.ID,
		Targets: []string{}, Strategy: "round"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelTargetRequired)
	_, err = tunnels.Create(&dto.CreateTunnelReq{Name: "invalid Clash", EntryRuleID: source.ID, ExitRuleID: destination.ID,
		UseClashRuleset: true, Strategy: "round"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleClashInvalid)
	clashSource := model.GostRule{Name: "local", NodeID: &first.ID, ListenIP: "127.0.0.1", ListenPort: 18003}
	saveServiceFixtures(t, db, &clashSource)
	tunnel := model.GostTunnel{Name: "Clash", EntryNodeID: first.ID, ExitNodeID: second.ID,
		EntryRuleID: &clashSource.ID, ExitRuleID: &destination.ID, UseClashRuleset: true, Strategy: "round"}
	saveServiceFixtures(t, db, &tunnel)
	_, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "invalid new source", EntryRuleID: source.ID, ExitRuleID: destination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleClashInvalid)
	stored, err := tunnels.GetByID(tunnel.ID)
	requireServiceError(t, err, nil)
	if *stored.EntryRuleID != clashSource.ID || !stored.UseClashRuleset {
		t.Fatal("failed validation replaced the saved source or target type")
	}
}

func TestEndpointUpdatePreservesOmittedLegacyTargets(t *testing.T) {
	db := newServiceTestDB(t)
	node := model.GostNode{Name: "first"}
	saveServiceFixtures(t, db, &node)
	rule := model.GostRule{Name: "legacy", NodeID: &node.ID, ListenIP: "127.0.0.1", ListenPort: 18001,
		Targets: []string{"one.test:443", "two.test:443"}, Strategy: "hash"}
	saveServiceFixtures(t, db, &rule)
	rules := NewRuleService(db)
	request := &dto.UpdateRuleReq{Name: "renamed endpoint", Protocol: "tcp", ListenPort: rule.ListenPort}
	_, err := rules.Update(rule.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err := rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if !slices.Equal(stored.Targets, rule.Targets) || stored.Strategy != "hash" {
		t.Fatalf("endpoint-only update cleared existing target settings: %+v", stored)
	}
	clash := true
	request.UseClashRuleset = &clash
	_, err = rules.Update(rule.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrRuleClashInvalid)
	request.Targets = []string{}
	_, err = rules.Update(rule.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err = rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if len(stored.Targets) != 0 || !stored.UseClashRuleset {
		t.Fatalf("explicit legacy target update was ignored: %+v", stored)
	}
	listenIP := ""
	request.ListenIP, request.Protocol = &listenIP, "udp"
	request.UseClashRuleset, request.Targets = nil, nil
	_, err = rules.Update(rule.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err = rules.GetByID(rule.ID)
	requireServiceError(t, err, nil)
	if stored.ListenIP != "" || stored.Protocol != model.RuleProtocolUDP {
		t.Fatal("obsolete Clash settings prevented editing an unassigned endpoint listener")
	}
}

func TestLegacyTunnelTargetsRemainUsableUntilExplicitlyChanged(t *testing.T) {
	f := newTransparentTestTunnel(t)
	requireServiceError(t, f.start(), nil)
	stored, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	if stored.Strategy != "" || !stored.EntryRule.UseClashRuleset {
		t.Fatal("reading or restarting a legacy tunnel rewrote its configuration")
	}
	subscription := NewGostRulesetService(f.db)
	// GORM's serializer needs the model field selected when saving this slice.
	setMatchers := func(match string) {
		t.Helper()
		ruleset := model.GostRuleset{ID: gostRulesetID, Matchers: []string{match}}
		requireServiceError(t, f.db.Model(&ruleset).Select("Matchers").Updates(&ruleset).Error, nil)
	}
	setMatchers(".legacy-updated.test")
	subscription.sync(t.Context(), map[uint]bool{})
	var bypass gost.AdmissionConfig
	f.entryNode.read("/config/bypasses/"+gost.TransparentBypassName, &bypass)
	if !slices.Equal(bypass.Matchers, []string{".legacy-updated.test"}) {
		t.Fatalf("legacy Clash source was not synchronized: %+v", bypass)
	}
	requireServiceError(t, f.stop(), nil)
	_, err = f.service.Update(f.tunnel.ID, &dto.UpdateTunnelReq{Name: "promoted", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err = f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	if !stored.UseClashRuleset || stored.Strategy != "round" {
		t.Fatal("editing an old tunnel did not retain and promote its target settings")
	}
	_, err = f.service.Update(f.tunnel.ID, &dto.UpdateTunnelReq{Name: "fixed", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID,
		Targets: []string{"fixed.test:443"}, Strategy: "round"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	requireServiceError(t, f.start(), nil)
	forward := f.entryNode.service(entranceServiceName(f.source.ID))
	if forward.Forwarder == nil || forward.Forwarder.Nodes[0].Addr != "fixed.test:443" {
		t.Fatal("fixed tunnel settings did not override its source's old Clash flag")
	}
	setMatchers(".must-not-sync.test")
	subscription.sync(t.Context(), map[uint]bool{})
	f.entryNode.read("/config/bypasses/"+gost.TransparentBypassName, &bypass)
	if !slices.Equal(bypass.Matchers, []string{".legacy-updated.test"}) {
		t.Fatal("ruleset synchronization used an obsolete source flag after switching the tunnel to fixed targets")
	}
	requireServiceError(t, f.stop(), nil)
}

func TestLegacyTunnelValidatesClashSourceWhenStarting(t *testing.T) {
	f := newTransparentTestTunnel(t)
	requireServiceError(t, f.db.Model(&f.source).Update("listen_ip", "").Error, nil)
	requireServiceError(t, f.start(), bizerrors.ErrRuleClashInvalid)
	if f.entryNode.service(entranceServiceName(f.source.ID)).Name != "" || f.exitNode.service(relayServiceName(f.tunnel.ID)).Name != "" {
		t.Fatal("invalid legacy target configuration provisioned node services")
	}
}
