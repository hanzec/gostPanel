package service

import (
	"slices"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
)

func TestEndpointCreationValidatesRoleOwnedPolicy(t *testing.T) {
	db := newServiceTestDB(t)
	node := model.GostNode{Name: "endpoint node"}
	legacyTunnel := model.GostTunnel{Name: "legacy tunnel"}
	saveServiceFixtures(t, db, &node, &legacyTunnel)
	service := NewRuleService(db)
	for i, test := range []struct {
		name, role, protocol, listenIP string
		targets                        []string
		clash, legacy                  bool
		want                           error
	}{
		{name: "entry without targets", role: "entry", protocol: "tcp"},
		{name: "TLS exit with domain target", role: "exit", protocol: "tls", targets: []string{"origin.example:443"}},
		{name: "QUIC exit with Clash", role: "exit", protocol: "quic", clash: true},
		{name: "legacy tunnel cannot acquire endpoint role", role: "entry", protocol: "tcp", legacy: true, want: bizerrors.ErrEndpointRole},
		{name: "entry rejects TLS", role: "entry", protocol: "tls", want: bizerrors.ErrEndpointProtocol},
		{name: "entry rejects targets", role: "entry", protocol: "tcp", targets: []string{"origin.example:443"}, want: bizerrors.ErrEndpointTargets},
		{name: "entry rejects Clash", role: "entry", protocol: "tcp", clash: true, want: bizerrors.ErrEndpointTargets},
		{name: "exit rejects loopback", role: "exit", protocol: "tls", listenIP: "127.0.0.1", targets: []string{"origin.example:443"}, want: bizerrors.ErrTunnelExitInvalid},
		{name: "exit requires policy", role: "exit", protocol: "tcp", want: bizerrors.ErrEndpointTargets},
		{name: "exit rejects mixed policy", role: "exit", protocol: "tcp", targets: []string{"origin.example:443"}, clash: true, want: bizerrors.ErrEndpointTargets},
		{name: "exit rejects blank target", role: "exit", protocol: "tcp", targets: []string{" "}, want: bizerrors.ErrEndpointTargets},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := &dto.CreateRuleReq{Name: test.name, NodeID: &node.ID, Role: test.role, Protocol: test.protocol,
				ListenIP: test.listenIP, ListenPort: 19000 + i, Targets: test.targets, UseClashRuleset: test.clash, Strategy: "hash"}
			if test.legacy {
				request.Type, request.TunnelID = "tunnel", &legacyTunnel.ID
			}
			created, err := service.Create(request, testAdmin, testIP, testAgent)
			requireServiceError(t, err, test.want)
			if test.want != nil {
				var count int64
				requireServiceError(t, db.Model(&model.GostRule{}).Where("name = ?", test.name).Count(&count).Error, nil)
				if count != 0 {
					t.Fatal("invalid endpoint configuration was saved")
				}
				return
			}
			stored, err := service.GetByID(created.ID)
			requireServiceError(t, err, nil)
			if string(stored.Role) != test.role || string(stored.Protocol) != test.protocol ||
				!slices.Equal(stored.Targets, test.targets) || stored.UseClashRuleset != test.clash {
				t.Fatalf("role policy was not persisted: %+v", stored)
			}
			requireServiceError(t, service.Start(stored.ID, testAdmin, testIP, testAgent), bizerrors.ErrRuleTargetRequired)
		})
	}
}

func TestEndpointRoleConversionClearsEntryPolicyAndProtectsStoppedExitBinding(t *testing.T) {
	db := newServiceTestDB(t)
	node, otherNode := model.GostNode{Name: "first"}, model.GostNode{Name: "second"}
	saveServiceFixtures(t, db, &node, &otherNode)
	service := NewRuleService(db)
	legacy, err := service.Create(&dto.CreateRuleReq{Name: "legacy", NodeID: &node.ID, Protocol: "tcp", ListenPort: 19100,
		Targets: []string{"legacy.example:443"}, Strategy: "hash"}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	request := &dto.UpdateRuleReq{Name: "entry", Role: "entry", Protocol: "tcp", ListenPort: legacy.ListenPort}
	_, err = service.Update(legacy.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err := service.GetByID(legacy.ID)
	requireServiceError(t, err, nil)
	if stored.Role != model.EndpointRoleEntry || len(stored.Targets) != 0 || stored.UseClashRuleset || stored.Strategy != "round" {
		t.Fatalf("converting a legacy listener to entry retained obsolete target policy: %+v", stored)
	}
	clash := true
	for _, policy := range []*dto.UpdateRuleReq{
		{Name: "bad entry", Protocol: "tcp", ListenPort: legacy.ListenPort, Targets: []string{"origin.example:443"}},
		{Name: "bad entry", Protocol: "tcp", ListenPort: legacy.ListenPort, UseClashRuleset: &clash},
	} {
		_, err = service.Update(legacy.ID, policy, testAdmin, testIP, testAgent)
		requireServiceError(t, err, bizerrors.ErrEndpointTargets)
	}
	exit, err := service.Create(&dto.CreateRuleReq{Name: "old exit", NodeID: &otherNode.ID, Protocol: "tcp", ListenPort: 19101,
		Targets: []string{"origin.example:443"}}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	_, err = service.Update(exit.ID, &dto.UpdateRuleReq{Name: "invalid legacy transport", Protocol: "tls", ListenPort: exit.ListenPort}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrEndpointProtocol)
	request = &dto.UpdateRuleReq{Name: "exit", Role: "exit", Protocol: "tls", ListenPort: exit.ListenPort, Strategy: "fifo"}
	_, err = service.Update(exit.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	tunnel := model.GostTunnel{Name: "bound", EntryNodeID: node.ID, ExitNodeID: otherNode.ID, EntryRuleID: &legacy.ID, ExitRuleID: &exit.ID}
	saveServiceFixtures(t, db, &tunnel)
	for _, change := range []func(*dto.UpdateRuleReq){
		func(r *dto.UpdateRuleReq) { r.NodeID = &node.ID },
		func(r *dto.UpdateRuleReq) { r.Role = "entry" },
		func(r *dto.UpdateRuleReq) { r.ListenPort++ },
		func(r *dto.UpdateRuleReq) { ip := "192.0.2.9"; r.ListenIP = &ip },
	} {
		invalid := *request
		change(&invalid)
		_, err = service.Update(exit.ID, &invalid, testAdmin, testIP, testAgent)
		requireServiceError(t, err, bizerrors.ErrRuleTunnelLinked)
	}
	request.Protocol, request.Targets = "quic", []string{"new.example:8443"}
	_, err = service.Update(exit.ID, request, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	stored, err = service.GetByID(exit.ID)
	requireServiceError(t, err, nil)
	if stored.Role != model.EndpointRoleExit || stored.Protocol != "quic" || !slices.Equal(stored.Targets, request.Targets) ||
		stored.Strategy != "fifo" || stored.NodeID == nil || *stored.NodeID != otherNode.ID || stored.ListenPort != exit.ListenPort || stored.ListenIP != "" {
		t.Fatalf("editing a stopped exit failed to retain its binding and save policy: %+v", stored)
	}
}
