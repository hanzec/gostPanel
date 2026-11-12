package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"yagp/internal/model"

	"gorm.io/gorm"
)

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
	db := legacyEndpointDatabase(t)
	stamp := time.Now().UTC()
	insertLegacyEndpoint(t, db, 11, "source", "tcp", []string{"legacy.example:443"}, false, "hash", stamp)
	insertLegacyEndpoint(t, db, 12, "destination", "tcp", nil, false, "round", stamp)
	if err := db.Exec(`INSERT INTO tunnels (id, name, entry_node_id, exit_node_id, entry_rule_id, exit_rule_id, protocol, strategy) VALUES (21, 'existing', 1, 2, 11, 12, 'tls', '')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_entry_migration BEFORE UPDATE ON rules WHEN OLD.id = 11 BEGIN SELECT RAISE(ABORT, 'entry update failed'); END`).Error; err != nil {
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
