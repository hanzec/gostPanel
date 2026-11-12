package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yagp/internal/model"

	"gorm.io/gorm"
)

func TestServerMigratesLegacyRelayOptionsToExit(t *testing.T) {
	db := legacyEndpointDatabase(t)
	stamp := time.Now().UTC()
	insertLegacyEndpoint(t, db, 11, "source", "tcp", []string{"origin.test:443"}, false, "round", stamp)
	insertLegacyEndpoint(t, db, 12, "destination", "udp", nil, false, "round", stamp)
	entryID, exitID := uint(11), uint(12)
	options := map[string]string{"path": "/migrated", "tls_server_name": "migrated.example"}
	tunnel := model.GostTunnel{Name: "existing options", EntryNodeID: 1, ExitNodeID: 2, EntryRuleID: &entryID, ExitRuleID: &exitID,
		Protocol: "wss", ProtocolOptions: options, RelayPort: 8443}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	if err := autoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var exit model.GostRule
	if err := db.First(&exit, exitID).Error; err != nil {
		t.Fatal(err)
	}
	if exit.Role != model.EndpointRoleExit || exit.Protocol != "wss" || !maps.Equal(exit.ProtocolOptions, options) {
		t.Fatalf("migration lost relay options: %+v", exit)
	}
	exit.ProtocolOptions = map[string]string{"path": "/edited"}
	if err := db.Model(&exit).Select("ProtocolOptions").Updates(&exit).Error; err != nil {
		t.Fatal(err)
	}
	if err := autoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var reloaded model.GostRule
	if err := db.First(&reloaded, exitID).Error; err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(reloaded.ProtocolOptions, exit.ProtocolOptions) {
		t.Fatalf("restart restored stale tunnel options: %v", reloaded.ProtocolOptions)
	}
}

func TestServerAddsRelayOptionsColumnsWithoutChangingOldEndpoints(t *testing.T) {
	db := legacyEndpointDatabase(t)
	stamp := time.Now().UTC()
	insertLegacyEndpoint(t, db, 11, "standalone", "tcp", []string{"origin.test:443"}, false, "round", stamp)
	for _, record := range []any{&model.GostRule{}, &model.GostTunnel{}} {
		if err := db.Migrator().DropColumn(record, "ProtocolOptions"); err != nil {
			t.Fatal(err)
		}
	}
	if err := autoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var rule model.GostRule
	if err := db.First(&rule, 11).Error; err != nil {
		t.Fatal(err)
	}
	if len(rule.ProtocolOptions) != 0 || rule.Protocol != "tcp" || rule.Role != "" || !rule.UpdatedAt.Equal(stamp) {
		t.Fatalf("new options column changed legacy endpoint: %+v", rule)
	}
}

func TestServerMigratesOriginalTargetPolicyToExit(t *testing.T) {
	for _, sourceOwned := range []bool{false, true} {
		t.Run(fmt.Sprintf("source owned %t", sourceOwned), func(t *testing.T) {
			db := legacyEndpointDatabase(t)
			stamp := time.Now().UTC()
			insertLegacyEndpoint(t, db, 11, "source", "red", nil, false, "round", stamp)
			insertLegacyEndpoint(t, db, 12, "destination", "tls", nil, false, "round", stamp)
			entryID, exitID := uint(11), uint(12)
			tunnel := model.GostTunnel{Name: "original targets", EntryNodeID: 1, ExitNodeID: 2, EntryRuleID: &entryID, ExitRuleID: &exitID,
				Protocol: "tls", Strategy: "round", UseOriginalTarget: true, RulesetIDs: []uint{2, 3}, ManualDomains: []string{"example.test"}}
			if sourceOwned {
				policy := model.GostRule{UseOriginalTarget: true, RulesetIDs: tunnel.RulesetIDs, ManualDomains: tunnel.ManualDomains}
				if err := db.Model(&model.GostRule{}).Where("id = ?", entryID).Select("UseOriginalTarget", "RulesetIDs", "ManualDomains").UpdateColumns(&policy).Error; err != nil {
					t.Fatal(err)
				}
				tunnel.Strategy, tunnel.UseOriginalTarget, tunnel.RulesetIDs, tunnel.ManualDomains = "", false, nil, nil
			}
			if err := db.Create(&tunnel).Error; err != nil {
				t.Fatal(err)
			}
			if err := autoMigrate(db); err != nil {
				t.Fatal(err)
			}
			var source, exit model.GostRule
			if err := db.First(&source, entryID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&exit, exitID).Error; err != nil {
				t.Fatal(err)
			}
			if source.Role != model.EndpointRoleEntry || source.Protocol != model.RuleProtocolTransparent || source.UseOriginalTarget || len(source.RulesetIDs) != 0 || len(source.ManualDomains) != 0 ||
				exit.Role != model.EndpointRoleExit || !exit.UseOriginalTarget || !slices.Equal(exit.RulesetIDs, []uint{2, 3}) || !slices.Equal(exit.ManualDomains, []string{"example.test"}) {
				t.Fatalf("original-target migration lost or duplicated policy: source=%+v exit=%+v", source, exit)
			}
		})
	}
}

func TestServerMigratesTunnelTargetsToExitEndpoints(t *testing.T) {
	for _, test := range []struct {
		name           string
		tunnelTargets  []string
		tunnelClash    bool
		tunnelStrategy string
		tunnelProtocol string
		entryTargets   []string
		entryClash     bool
		entryStrategy  string
		exitProtocol   string
		wantTargets    []string
		wantClash      bool
		wantStrategy   string
		wantProtocol   model.RuleProtocol
	}{
		{
			name: "tunnel fixed targets", tunnelTargets: []string{"one.example:443", "two.example:8443"},
			tunnelStrategy: "hash", tunnelProtocol: "tls", entryTargets: []string{"stale.example:80"}, entryStrategy: "rand", exitProtocol: "tcp",
			wantTargets: []string{"one.example:443", "two.example:8443"}, wantStrategy: "hash", wantProtocol: "tls",
		},
		{
			name: "tunnel Clash policy", tunnelTargets: []string{}, tunnelClash: true, tunnelStrategy: "fifo", tunnelProtocol: "quic",
			entryTargets: []string{"stale.example:80"}, entryStrategy: "round", exitProtocol: "tcp",
			wantTargets: []string{}, wantClash: true, wantStrategy: "fifo", wantProtocol: "quic",
		},
		{
			name: "legacy source fixed targets", tunnelTargets: []string{"ignored.example:80"},
			entryTargets: []string{"legacy.example:443"}, entryStrategy: "hash", exitProtocol: "mtls",
			wantTargets: []string{"legacy.example:443"}, wantStrategy: "hash", wantProtocol: "mtls",
		},
		{
			name: "legacy source Clash policy", tunnelTargets: []string{"ignored.example:80"}, tunnelProtocol: "ws",
			entryTargets: []string{}, entryClash: true, entryStrategy: "rand", exitProtocol: "tcp",
			wantTargets: []string{}, wantClash: true, wantStrategy: "rand", wantProtocol: "ws",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := legacyEndpointDatabase(t)
			stamp := time.Date(2026, time.November, 12, 8, 0, 0, 0, time.UTC)
			insertLegacyEndpoint(t, db, 11, "source", "tcp", test.entryTargets, test.entryClash, test.entryStrategy, stamp)
			insertLegacyEndpoint(t, db, 12, "destination", test.exitProtocol, []string{"unused.example:80"}, false, "round", stamp)
			insertLegacyEndpoint(t, db, 13, "unbound", "udp", []string{"standalone.example:53"}, false, "fifo", stamp)
			if err := db.Exec(`INSERT INTO tunnels
				(id, name, entry_node_id, exit_node_id, entry_rule_id, exit_rule_id, protocol, relay_port, targets, use_clash_ruleset, strategy, status, service_id, chain_id, relay_secret, updated_at)
				VALUES (21, 'existing tunnel', 1, 2, 11, 12, ?, 8443, ?, ?, ?, 'running', 'relay-21', 'chain-21', 'existing-secret', ?)`,
				test.tunnelProtocol, jsonTargets(t, test.tunnelTargets), test.tunnelClash, test.tunnelStrategy, stamp).Error; err != nil {
				t.Fatal(err)
			}
			if err := autoMigrate(db); err != nil {
				t.Fatal(err)
			}
			var entry, exit, unbound model.GostRule
			for id, endpoint := range map[uint]*model.GostRule{11: &entry, 12: &exit, 13: &unbound} {
				if err := db.First(endpoint, id).Error; err != nil {
					t.Fatal(err)
				}
				if endpoint.Status != model.RuleStatusRunning || endpoint.ServiceID != "service-"+endpoint.Name || endpoint.ObserverID != "observer-"+endpoint.Name || !endpoint.UpdatedAt.Equal(stamp) {
					t.Fatalf("migration changed live endpoint identity or state: %+v", endpoint)
				}
			}
			if entry.Role != model.EndpointRoleEntry || entry.Protocol != model.RuleProtocolTCP || entry.Targets == nil || len(entry.Targets) != 0 || entry.UseClashRuleset || entry.Strategy != "round" {
				t.Fatalf("migrated entry = %+v", entry)
			}
			if exit.Role != model.EndpointRoleExit || exit.Protocol != test.wantProtocol || !slices.Equal(exit.Targets, test.wantTargets) || exit.UseClashRuleset != test.wantClash || exit.Strategy != test.wantStrategy {
				t.Fatalf("migrated exit = %+v", exit)
			}
			if unbound.Role != "" || unbound.Protocol != model.RuleProtocolUDP || !slices.Equal(unbound.Targets, []string{"standalone.example:53"}) || unbound.UseClashRuleset || unbound.Strategy != "fifo" {
				t.Fatalf("unbound legacy endpoint changed: %+v", unbound)
			}
			var tunnel model.GostTunnel
			if err := db.First(&tunnel, 21).Error; err != nil {
				t.Fatal(err)
			}
			if tunnel.Status != model.TunnelStatusRunning || tunnel.ServiceID != "relay-21" || tunnel.ChainID != "chain-21" || tunnel.RelaySecret != "existing-secret" || !tunnel.UpdatedAt.Equal(stamp) ||
				tunnel.Protocol != test.tunnelProtocol || !slices.Equal(tunnel.Targets, test.tunnelTargets) || tunnel.UseClashRuleset != test.tunnelClash || tunnel.Strategy != test.tunnelStrategy {
				t.Fatalf("migration changed the existing tunnel: %+v", tunnel)
			}

			exit.Targets, exit.UseClashRuleset, exit.Strategy, exit.Protocol = []string{"edited.example:9443"}, false, "round", "mws"
			if err := db.Model(&exit).Select("Targets", "UseClashRuleset", "Strategy", "Protocol").UpdateColumns(&exit).Error; err != nil {
				t.Fatal(err)
			}
			if err := autoMigrate(db); err != nil {
				t.Fatal(err)
			}
			var restartedExit model.GostRule
			if err := db.First(&restartedExit, exit.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restartedExit, exit) {
				t.Fatalf("restart replaced the edited exit: got %+v, want %+v", restartedExit, exit)
			}
		})
	}
}

func TestEndpointRoleMigrationRollsBackBothEndpoints(t *testing.T) {
	for _, rejectedID := range []uint{11, 12} {
		t.Run(fmt.Sprintf("reject endpoint %d", rejectedID), func(t *testing.T) {
			db := legacyEndpointDatabase(t)
			stamp := time.Now().UTC()
			insertLegacyEndpoint(t, db, 11, "source", "tcp", []string{"legacy.example:443"}, false, "hash", stamp)
			insertLegacyEndpoint(t, db, 12, "destination", "tcp", nil, false, "round", stamp)
			if err := db.Exec(`INSERT INTO tunnels (id, name, entry_node_id, exit_node_id, entry_rule_id, exit_rule_id, protocol, strategy) VALUES (21, 'existing', 1, 2, 11, 12, 'tls', '')`).Error; err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf(`CREATE TRIGGER reject_endpoint_migration BEFORE UPDATE ON rules WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'endpoint update failed'); END`, rejectedID)
			if err := db.Exec(trigger).Error; err != nil {
				t.Fatal(err)
			}
			if err := autoMigrate(db); err == nil {
				t.Fatal("endpoint migration unexpectedly succeeded")
			}
			var entry, exit model.GostRule
			if err := db.First(&entry, 11).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&exit, 12).Error; err != nil {
				t.Fatal(err)
			}
			if entry.Role != "" || exit.Role != "" || exit.Protocol != "tcp" || len(exit.Targets) != 0 || !slices.Equal(entry.Targets, []string{"legacy.example:443"}) {
				t.Fatalf("failed migration left partial changes: entry=%+v, exit=%+v", entry, exit)
			}
		})
	}
}

func TestEndpointRoleMigrationLeavesIncompleteBindingsUnchanged(t *testing.T) {
	for _, missingID := range []uint{11, 12} {
		t.Run(fmt.Sprintf("missing endpoint %d", missingID), func(t *testing.T) {
			db := legacyEndpointDatabase(t)
			stamp := time.Now().UTC()
			insertLegacyEndpoint(t, db, 11, "source", "tcp", []string{"legacy.example:443"}, false, "hash", stamp)
			insertLegacyEndpoint(t, db, 12, "destination", "tcp", nil, false, "round", stamp)
			if err := db.Exec(`INSERT INTO tunnels (id, name, entry_node_id, exit_node_id, entry_rule_id, exit_rule_id, protocol, strategy) VALUES (21, 'incomplete', 1, 2, 11, 12, 'tls', '')`).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("DELETE FROM rules WHERE id = ?", missingID).Error; err != nil {
				t.Fatal(err)
			}
			var before model.GostRule
			if err := db.First(&before).Error; err != nil {
				t.Fatal(err)
			}
			if err := autoMigrate(db); err != nil {
				t.Fatal(err)
			}
			var after model.GostRule
			if err := db.First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("incomplete tunnel changed its surviving endpoint: got %+v, want %+v", after, before)
			}
		})
	}
}

func TestEndpointRoleMigrationRejectsInvalidStoredData(t *testing.T) {
	for _, test := range []struct {
		name, targets, extraDDL, wantError string
	}{
		{name: "malformed targets", targets: "not-json", wantError: "invalid character"},
		{name: "incomplete earlier allowlist migration", targets: "[]", extraDDL: "CREATE TABLE gost_allowlists (id integer PRIMARY KEY)", wantError: "node_ids"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := legacyEndpointDatabase(t)
			stamp := time.Now().UTC()
			insertLegacyEndpoint(t, db, 11, "source", "tcp", []string{"legacy.example:443"}, false, "hash", stamp)
			insertLegacyEndpoint(t, db, 12, "destination", "tcp", nil, false, "round", stamp)
			if err := db.Exec(`INSERT INTO tunnels (id, name, entry_node_id, exit_node_id, entry_rule_id, exit_rule_id, protocol, strategy, targets) VALUES (21, 'existing', 1, 2, 11, 12, 'tls', 'round', ?)`, test.targets).Error; err != nil {
				t.Fatal(err)
			}
			if test.extraDDL != "" {
				if err := db.Exec(test.extraDDL).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := autoMigrate(db); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("migration error = %v, want %q", err, test.wantError)
			}
			var entry, exit model.GostRule
			if err := db.First(&entry, 11).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&exit, 12).Error; err != nil {
				t.Fatal(err)
			}
			if entry.Role != "" || exit.Role != "" || exit.Protocol != "tcp" || len(exit.Targets) != 0 || !slices.Equal(entry.Targets, []string{"legacy.example:443"}) {
				t.Fatalf("invalid stored data caused endpoint changes: entry=%+v, exit=%+v", entry, exit)
			}
		})
	}
}

func legacyEndpointDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db := testSQLite(t)
	if err := db.AutoMigrate(&model.GostNode{}, &model.GostRule{}, &model.GostTunnel{}); err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-role schema using the same database dialect and existing model columns.
	if err := db.Migrator().DropColumn(&model.GostRule{}, "Role"); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertLegacyEndpoint(t *testing.T, db *gorm.DB, id uint, name, protocol string, targets []string, useClash bool, strategy string, stamp time.Time) {
	t.Helper()
	if err := db.Exec(`INSERT INTO rules (id, name, protocol, listen_port, targets, use_clash_ruleset, strategy, status, service_id, observer_id, updated_at)
		VALUES (?, ?, ?, 8443, ?, ?, ?, 'running', ?, ?, ?)`, id, name, protocol, jsonTargets(t, targets), useClash, strategy, "service-"+name, "observer-"+name, stamp).Error; err != nil {
		t.Fatal(err)
	}
}

func jsonTargets(t *testing.T, targets []string) string {
	t.Helper()
	encoded, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
