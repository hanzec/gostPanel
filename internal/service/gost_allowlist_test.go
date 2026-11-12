package service

import (
	"slices"
	"testing"

	"yagp/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGostAllowlistValidatesNodeSelection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.GostNode{}); err != nil {
		t.Fatal(err)
	}
	nodes := []model.GostNode{
		{Name: "IPv4", IPv4: "192.0.2.1", Port: 39000},
		{Name: "IPv6", IPv6: "2001:db8::1", Port: 39000},
		{Name: "old address", Address: "198.51.100.2", Port: 39000},
		{Name: "dual stack", IPv4: "192.0.2.2", IPv6: "2001:db8::2", Port: 39000, ManualAddresses: []string{"203.0.113.2", "192.0.2.2"}},
		{Name: "manual only", ManualAddresses: []string{"203.0.113.3"}},
		{Name: "snapshot", Addresses: []string{"192.0.2.4", "2001:db8::4"}, ManualAddresses: []string{"203.0.113.4", "192.0.2.4"}},
	}
	if err = db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	service := NewGostAllowlistService(db)
	ids, err := service.validateNodeIDs(ctx, []uint{nodes[1].ID, nodes[0].ID, nodes[1].ID})
	if err != nil || !slices.Equal(ids, []uint{nodes[0].ID, nodes[1].ID}) {
		t.Fatalf("validated node IDs = %v, err = %v", ids, err)
	}
	if _, err = service.validateNodeIDs(ctx, []uint{nodes[2].ID}); err == nil {
		t.Fatal("accepted a node without a registered IP address")
	}
	if _, err = service.validateNodeIDs(ctx, []uint{0}); err == nil {
		t.Fatal("accepted an unknown node ID")
	}
	addresses, err := service.resolveAddresses(ctx, []uint{nodes[3].ID}, false)
	if err != nil || !slices.Equal(addresses, []string{"192.0.2.2", "2001:db8::2", "203.0.113.2"}) {
		t.Fatalf("resolved addresses = %v, err = %v", addresses, err)
	}
	for _, i := range []int{4, 5} {
		if _, err := service.validateNodeIDs(ctx, []uint{nodes[i].ID}); err != nil {
			t.Fatalf("node with supplemental addresses rejected: %v", err)
		}
	}
	addresses, err = service.resolveAddresses(ctx, []uint{nodes[4].ID, nodes[5].ID}, false)
	if err != nil || !slices.Equal(addresses, []string{"192.0.2.4", "2001:db8::4", "203.0.113.3", "203.0.113.4"}) {
		t.Fatalf("snapshot and manual addresses were not merged: %v, err = %v", addresses, err)
	}
	ids, err = service.validateNodeIDs(ctx, nil)
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("cleared selection = %v, err = %v", ids, err)
	}
}
