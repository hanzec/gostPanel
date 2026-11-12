package service

import (
	"encoding/json"
	"slices"
	"testing"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/pkg/gost"
)

func TestAutomaticTunnelAllowlistDefaultsAndLegacySelections(t *testing.T) {
	for _, endpointBased := range []bool{false, true} {
		for _, selection := range []string{"automatic", "explicit empty", "explicit source"} {
			name := "nodes/" + selection
			if endpointBased {
				name = "endpoints/" + selection
			}
			t.Run(name, func(t *testing.T) {
				db := newServiceTestDB(t)
				loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
				entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
				entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
				entry.IPv4, entry.IPv6 = "192.0.2.1", "2001:db8::1"
				entry.ManualAddresses, entry.LocalAddresses = []string{"203.0.113.1", entry.IPv4}, []string{"10.0.0.1", "fd00::1"}
				exit.Name = "exit"
				other := model.GostNode{Name: "manual source", IPv4: "192.0.2.2", LocalAddresses: []string{"10.0.0.2"}}
				saveServiceFixtures(t, db, &entry, &exit, &other)
				req := &dto.CreateTunnelReq{Name: name, EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 18443}
				if endpointBased {
					source := model.GostRule{Name: "source", NodeID: &entry.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleEntry, Protocol: "tcp", ListenPort: 18001}
					destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleExit, Protocol: "tcp", ListenPort: 18443,
						Targets: []string{"origin.test:443"}}
					saveServiceFixtures(t, db, &source, &destination)
					req.EntryNodeID, req.ExitNodeID, req.RelayPort = 0, 0, 0
					req.EntryRuleID, req.ExitRuleID = source.ID, destination.ID
				}
				want := []string{"10.0.0.1", "192.0.2.1", "2001:db8::1", "203.0.113.1", "fd00::1"}
				switch selection {
				case "explicit empty":
					req.AllowlistNodeIDs, want = []uint{}, nil
				case "explicit source":
					req.AllowlistNodeIDs, want = []uint{other.ID}, []string{other.IPv4}
				}
				tunnels := NewTunnelService(db)
				tunnel, err := tunnels.Create(req, testAdmin, testIP, testAgent)
				requireServiceError(t, err, nil)
				requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
				stored, err := tunnels.GetByID(tunnel.ID)
				requireServiceError(t, err, nil)
				if stored.AutoAllowlist != (selection == "automatic") || !slices.Equal(stored.AllowlistNodeIDs, req.AllowlistNodeIDs) {
					t.Fatalf("allowlist mode did not persist: %+v", stored)
				}
				relay := exitRemote.service(stored.ServiceID)
				admission := exitRemote.admission(gost.TunnelRelayAllowlistName(stored.ServiceID))
				if len(want) == 0 {
					if relay.Admission != "" || admission.Name != "" {
						t.Fatalf("explicit empty legacy selection acquired an admission: %+v, %+v", relay, admission)
					}
				} else if !relay.HasAdmission(admission.Name) || !admission.Whitelist || !slices.Equal(admission.Matchers, want) {
					t.Fatalf("relay source list = %+v, want %v", admission, want)
				}
				if stored.AutoAllowlist {
					assertAutomaticRelayAuth(t, entryRemote, exitRemote, stored)
				}
				requireServiceError(t, tunnels.Stop(stored.ID, testAdmin, testIP, testAgent), nil)
			})
		}
	}
}

func TestAutomaticAllowlistWaitsForAddressesAndRetainsRunningSnapshot(t *testing.T) {
	for _, endpointBased := range []bool{false, true} {
		name := "nodes"
		if endpointBased {
			name = "endpoints"
		}
		t.Run(name, func(t *testing.T) {
			db := newServiceTestDB(t)
			loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
			entryRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}
			entry, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, exitRemote)
			exit.Name = "exit"
			saveServiceFixtures(t, db, &entry, &exit)
			req := &dto.CreateTunnelReq{Name: name, EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 18443}
			if endpointBased {
				source := model.GostRule{Name: "source", NodeID: &entry.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleEntry, Protocol: "tcp", ListenPort: 18001}
				destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleExit, Protocol: "tcp", ListenPort: 18443,
					Targets: []string{"origin.test:443"}}
				saveServiceFixtures(t, db, &source, &destination)
				req.EntryNodeID, req.ExitNodeID, req.RelayPort = 0, 0, 0
				req.EntryRuleID, req.ExitRuleID = source.ID, destination.ID
			}
			tunnels := NewTunnelService(db)
			tunnel, err := tunnels.Create(req, testAdmin, testIP, testAgent)
			requireServiceError(t, err, nil)
			requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
			tunnel, err = tunnels.GetByID(tunnel.ID)
			requireServiceError(t, err, nil)
			assertAutomaticRelayAuth(t, entryRemote, exitRemote, tunnel)
			admissionName := gost.TunnelRelayAllowlistName(tunnel.ServiceID)
			if exitRemote.service(tunnel.ServiceID).HasAdmission(admissionName) || exitRemote.admission(admissionName).Name != "" {
				t.Fatal("entry without source addresses acquired a blocking admission")
			}
			sync := NewRuleSyncService(db)
			synchronize := func() {
				refreshAutomaticAllowlistConfig(t, exitRemote, tunnel.ServiceID)
				sync.syncNodeRules(exit, nil)
			}
			synchronize()
			if exitRemote.admission(admissionName).Name != "" {
				t.Fatal("sync created an empty automatic allowlist before the first address report")
			}
			requireServiceError(t, NewNodeService(db, "").UpdateIPs(t.Context(), &entry, "192.0.2.9", "", []string{"192.0.2.9"}, []string{"10.0.0.9"}, false), nil)
			synchronize()
			want := []string{"10.0.0.9", "192.0.2.9"}
			admission := exitRemote.admission(admissionName)
			if !exitRemote.service(tunnel.ServiceID).HasAdmission(admissionName) || !admission.Whitelist || !slices.Equal(admission.Matchers, want) {
				t.Fatalf("first address report did not activate the source allowlist: %+v", admission)
			}
			requireServiceError(t, db.Model(&entry).Select("IPv4", "IPv6", "Addresses", "LocalAddresses").Updates(&model.GostNode{}).Error, nil)
			synchronize()
			if admission = exitRemote.admission(admissionName); !admission.Whitelist || !slices.Equal(admission.Matchers, want) || !exitRemote.service(tunnel.ServiceID).HasAdmission(admissionName) {
				t.Fatalf("missing address snapshot changed the running allowlist: %+v", admission)
			}
			requireServiceError(t, NewNodeService(db, "").UpdateIPs(t.Context(), &entry, "192.0.2.10", "", []string{"192.0.2.10"}, []string{"10.0.0.10"}, false), nil)
			synchronize()
			if admission = exitRemote.admission(admissionName); !slices.Equal(admission.Matchers, []string{"10.0.0.10", "192.0.2.10"}) {
				t.Fatalf("changed addresses did not replace the automatic allowlist: %+v", admission)
			}
			requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
		})
	}
}

func TestAutomaticAllowlistFollowsChangedEntranceAndWaitsForItsReport(t *testing.T) {
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryRemote, replacementRemote, exitRemote := &lifecycleNode{t: t}, &lifecycleNode{t: t}, &lifecycleNode{t: t}
	entry, replacement, exit := startServiceTestNode(t, entryRemote), startServiceTestNode(t, replacementRemote), startServiceTestNode(t, exitRemote)
	entry.IPv4, replacement.Name, exit.Name = "192.0.2.1", "replacement", "exit"
	saveServiceFixtures(t, db, &entry, &replacement, &exit)
	source := model.GostRule{Name: "source", NodeID: &entry.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleEntry, Protocol: "tcp", ListenPort: 18001}
	nextSource := model.GostRule{Name: "next source", NodeID: &replacement.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleEntry, Protocol: "tcp", ListenPort: 18002}
	destination := model.GostRule{Name: "destination", NodeID: &exit.ID, Type: model.RuleTypeForward, Role: model.EndpointRoleExit, Protocol: "tcp", ListenPort: 18443,
		Targets: []string{"origin.test:443"}}
	saveServiceFixtures(t, db, &source, &nextSource, &destination)
	tunnels := NewTunnelService(db)
	tunnel, err := tunnels.Create(&dto.CreateTunnelReq{Name: "automatic entry", EntryRuleID: source.ID, ExitRuleID: destination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	admissionName := gost.TunnelRelayAllowlistName(tunnel.ServiceID)
	if got := exitRemote.admission(admissionName).Matchers; !slices.Equal(got, []string{entry.IPv4}) {
		t.Fatalf("initial automatic sources = %v", got)
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
	tunnel, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "changed entry", EntryRuleID: nextSource.ID, ExitRuleID: destination.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if !tunnel.AutoAllowlist || tunnel.EntryNodeID != replacement.ID {
		t.Fatalf("changing the entrance lost automatic mode: %+v", tunnel)
	}
	refreshAutomaticAllowlistConfig(t, exitRemote, tunnel.ServiceID)
	requireServiceError(t, tunnels.Start(tunnel.ID, testAdmin, testIP, testAgent), nil)
	if exitRemote.service(tunnel.ServiceID).HasAdmission(admissionName) {
		t.Fatal("new entrance without an address inherited the previous entrance's admission")
	}
	assertAutomaticRelayAuth(t, replacementRemote, exitRemote, tunnel)
	// A private-only report is sufficient for a LAN/VPN entrance.
	requireServiceError(t, NewNodeService(db, "").UpdateIPs(t.Context(), &replacement, "", "", nil, []string{"10.2.0.2", "fd02::2"}, false), nil)
	refreshAutomaticAllowlistConfig(t, exitRemote, tunnel.ServiceID)
	NewRuleSyncService(db).syncNodeRules(exit, nil)
	if got := exitRemote.admission(admissionName).Matchers; !slices.Equal(got, []string{"10.2.0.2", "fd02::2"}) || !exitRemote.service(tunnel.ServiceID).HasAdmission(admissionName) {
		t.Fatalf("automatic sources did not follow the replacement entry: %v", got)
	}
	requireServiceError(t, tunnels.Stop(tunnel.ID, testAdmin, testIP, testAgent), nil)
}

func TestLegacyAllowlistUpdatesLeaveAutomaticMode(t *testing.T) {
	for _, separateEndpoint := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			db := newServiceTestDB(t)
			entry, exit, selected := model.GostNode{Name: "entry"}, model.GostNode{Name: "exit"}, model.GostNode{Name: "selected", IPv4: "192.0.2.1"}
			saveServiceFixtures(t, db, &entry, &exit, &selected)
			tunnels := NewTunnelService(db)
			tunnel, err := tunnels.Create(&dto.CreateTunnelReq{Name: "automatic", EntryNodeID: entry.ID, ExitNodeID: exit.ID, Protocol: "tcp", RelayPort: 18443}, testAdmin, testIP, testAgent)
			requireServiceError(t, err, nil)
			ids := []uint{selected.ID}
			if empty {
				ids = []uint{}
			}
			if separateEndpoint {
				_, err = tunnels.UpdateAllowlist(t.Context(), tunnel.ID, ids, testAdmin, testIP, testAgent)
			} else {
				_, err = tunnels.Update(tunnel.ID, &dto.UpdateTunnelReq{Name: "explicit selection", Protocol: "tcp", RelayPort: 18443, AllowlistNodeIDs: ids}, testAdmin, testIP, testAgent)
			}
			requireServiceError(t, err, nil)
			stored, err := tunnels.GetByID(tunnel.ID)
			requireServiceError(t, err, nil)
			if stored.AutoAllowlist || !slices.Equal(stored.AllowlistNodeIDs, ids) {
				t.Fatalf("legacy update did not replace automatic mode: %+v", stored)
			}
		}
	}
}

func assertAutomaticRelayAuth(t *testing.T, entryRemote, exitRemote *lifecycleNode, tunnel *model.GostTunnel) {
	t.Helper()
	relay := exitRemote.service(tunnel.ServiceID)
	chain := entryRemote.chain(tunnel.ChainID)
	if relay.Handler == nil || relay.Handler.Auth == nil || relay.Handler.Auth.Password == "" ||
		len(chain.Hops) != 1 || len(chain.Hops[0].Nodes) != 1 || chain.Hops[0].Nodes[0].Connector.Auth == nil ||
		*relay.Handler.Auth != *chain.Hops[0].Nodes[0].Connector.Auth {
		t.Fatalf("automatic relay is not protected by matching credentials: relay=%+v chain=%+v", relay, chain)
	}
}

func refreshAutomaticAllowlistConfig(t *testing.T, remote *lifecycleNode, relayName string) {
	t.Helper()
	cfg := gost.GostConfig{}
	if service := remote.service(relayName); service.Name != "" {
		cfg.Services = append(cfg.Services, service)
	}
	if admission := remote.admission(gost.TunnelRelayAllowlistName(relayName)); admission.Name != "" {
		cfg.Admissions = append(cfg.Admissions, admission)
	}
	data, err := json.Marshal(cfg)
	requireServiceError(t, err, nil)
	remote.mu.Lock()
	remote.gostConfig = string(data)
	remote.mu.Unlock()
}
