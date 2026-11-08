package repository

import (
	"errors"
	"testing"

	"yagp/internal/model"

	"gorm.io/gorm"
)

func TestTunnelRepositoryEndpointAssociations(t *testing.T) {
	db := openRepositoryDB(t)
	tunnels := NewTunnelRepository(db)
	entryNode := model.GostNode{Name: "entry"}
	exitNode := model.GostNode{Name: "exit"}
	mustPersist(t, db.Create(&entryNode).Error)
	mustPersist(t, db.Create(&exitNode).Error)
	entry := model.GostRule{Name: "source", NodeID: &entryNode.ID, ListenPort: 443, Targets: []string{"example.test:443"}}
	exit := model.GostRule{Name: "destination", NodeID: &exitNode.ID, ListenPort: 18443}
	mustPersist(t, db.Create(&entry).Error)
	mustPersist(t, db.Create(&exit).Error)
	linked := model.GostTunnel{
		Name: "endpoint relay", EntryNodeID: entryNode.ID, ExitNodeID: exitNode.ID,
		EntryRuleID: &entry.ID, ExitRuleID: &exit.ID, Status: model.TunnelStatusRunning,
	}
	legacy := model.GostTunnel{
		Name: "legacy relay", EntryNodeID: exitNode.ID, ExitNodeID: entryNode.ID,
		Status: model.TunnelStatusRunning,
	}
	mustPersist(t, tunnels.Create(&linked))
	mustPersist(t, tunnels.Create(&legacy))
	rules := NewRuleRepository(db)
	bindings, err := rules.EntranceBindings([]uint{entry.ID, exit.ID})
	mustPersist(t, err)
	if len(bindings) != 1 || bindings[0].ID != linked.ID || *bindings[0].EntryRuleID != entry.ID || *bindings[0].ExitRuleID != exit.ID {
		t.Fatalf("bulk endpoint lookup = %+v", bindings)
	}
	mustPersist(t, rules.UpdateRuntime(exit.ID, "relay-test", model.RuleStatusRunning))
	currentExit, err := rules.FindByID(exit.ID)
	mustPersist(t, err)
	if currentExit.ServiceID != "relay-test" || currentExit.Status != model.RuleStatusRunning {
		t.Fatalf("runtime state was not saved together: %+v", currentExit)
	}

	for _, ruleID := range []uint{entry.ID, exit.ID} {
		got, err := tunnels.FindByEntranceID(ruleID)
		mustPersist(t, err)
		if got.ID != linked.ID {
			t.Fatalf("entrance %d resolved to tunnel %d, want %d", ruleID, got.ID, linked.ID)
		}
	}
	assertAssociations := func(got model.GostTunnel) {
		t.Helper()
		if got.EntryRule == nil || got.EntryRule.ID != entry.ID || got.ExitRule == nil || got.ExitRule.ID != exit.ID ||
			got.EntryNode == nil || got.EntryNode.ID != entryNode.ID || got.ExitNode == nil || got.ExitNode.ID != exitNode.ID {
			t.Fatalf("tunnel %d has incorrect endpoint associations: %+v", got.ID, got)
		}
	}
	detail, err := tunnels.FindByID(linked.ID)
	mustPersist(t, err)
	assertAssociations(*detail)
	list, total, err := tunnels.List(&QueryOption{Conditions: map[string]any{"id = ?": linked.ID}})
	mustPersist(t, err)
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected one linked tunnel, got %d of %d", len(list), total)
	}
	assertAssociations(list[0])

	mustPersist(t, tunnels.StopByNodeID(entryNode.ID))
	detail, err = tunnels.FindByID(linked.ID)
	mustPersist(t, err)
	old, err := tunnels.FindByID(legacy.ID)
	mustPersist(t, err)
	if detail.Status != model.TunnelStatusRunning || old.Status != model.TunnelStatusStopped {
		t.Fatalf("stop by node left endpoint tunnel %s and legacy tunnel %s", detail.Status, old.Status)
	}

	mustPersist(t, tunnels.Delete(linked.ID))
	bindings, err = rules.EntranceBindings([]uint{entry.ID, exit.ID})
	mustPersist(t, err)
	if len(bindings) != 0 {
		t.Fatalf("bulk lookup retained deleted binding: %+v", bindings)
	}
	for _, ruleID := range []uint{entry.ID, exit.ID} {
		if _, err := tunnels.FindByEntranceID(ruleID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("deleted tunnel still reserves entrance %d: %v", ruleID, err)
		}
	}
}
