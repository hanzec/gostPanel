package main

import (
	"yagp/internal/model"

	"gorm.io/gorm"
)

func migrateTunnelEndpointRoles(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var tunnels []model.GostTunnel
		if err := tx.Where("entry_rule_id IS NOT NULL AND exit_rule_id IS NOT NULL").
			Preload("EntryRule").Preload("ExitRule").Find(&tunnels).Error; err != nil {
			return err
		}
		for _, tunnel := range tunnels {
			entry, exit := tunnel.EntryRule, tunnel.ExitRule
			if entry == nil || exit == nil {
				continue
			}
			// The role marks migrated endpoints, so later startup cannot restore stale tunnel targets.
			if exit.Role == "" {
				exit.Targets, exit.UseClashRuleset, exit.Strategy = tunnel.Targets, tunnel.UseClashRuleset, tunnel.Strategy
				exit.UseOriginalTarget, exit.RulesetIDs, exit.ManualDomains = tunnel.UseOriginalTarget, tunnel.RulesetIDs, tunnel.ManualDomains
				if tunnel.Strategy == "" {
					exit.Targets, exit.UseClashRuleset, exit.Strategy = entry.Targets, entry.UseClashRuleset, entry.Strategy
					exit.UseOriginalTarget, exit.RulesetIDs, exit.ManualDomains = entry.UseOriginalTarget, entry.RulesetIDs, entry.ManualDomains
				}
				if tunnel.Protocol != "" {
					exit.Protocol = model.RuleProtocol(tunnel.Protocol)
					exit.ProtocolOptions = tunnel.ProtocolOptions
				}
				exit.Role = model.EndpointRoleExit
				if err := tx.Model(exit).Select("Role", "Protocol", "ProtocolOptions", "Targets", "UseClashRuleset", "UseOriginalTarget", "RulesetIDs", "ManualDomains", "Strategy").UpdateColumns(exit).Error; err != nil {
					return err
				}
			}
			if entry.Role == "" {
				entry.Role = model.EndpointRoleEntry
				entry.Targets, entry.UseClashRuleset, entry.Strategy = []string{}, false, "round"
				entry.UseOriginalTarget, entry.RulesetIDs, entry.ManualDomains = false, nil, nil
				if err := tx.Model(entry).Select("Role", "Targets", "UseClashRuleset", "UseOriginalTarget", "RulesetIDs", "ManualDomains", "Strategy").UpdateColumns(entry).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
