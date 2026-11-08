package service

import (
	"context"
	"slices"
	"testing"

	"yagp/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGostAllowlistPersistsValidNodeSelection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.GostNode{}, &model.GostAllowlist{}); err != nil {
		t.Fatal(err)
	}
	nodes := []model.GostNode{
		{Name: "IPv4", IPv4: "192.0.2.1", Port: 39000},
		{Name: "IPv6", IPv6: "2001:db8::1", Port: 39000},
		{Name: "old address", Address: "198.51.100.2", Port: 39000},
		{Name: "dual stack", IPv4: "192.0.2.2", IPv6: "2001:db8::2", Port: 39000},
	}
	if err = db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := NewGostAllowlistService(db)
	if _, err = service.Update(ctx, []uint{nodes[1].ID, nodes[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Update(ctx, []uint{nodes[2].ID}); err == nil {
		t.Fatal("accepted a node without a registered IP address")
	}
	config, err := service.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(config.NodeIDs, []uint{nodes[0].ID, nodes[1].ID}) ||
		!slices.Equal(config.Addresses, []string{"192.0.2.1", "2001:db8::1"}) {
		t.Fatalf("saved policy = %+v", config)
	}
	if _, err = service.Update(ctx, []uint{nodes[3].ID}); err != nil {
		t.Fatal(err)
	}
	config, err = service.Get(ctx)
	if err != nil || !slices.Equal(config.Addresses, []string{"192.0.2.2", "2001:db8::2"}) {
		t.Fatalf("detected policy = %+v, err = %v", config, err)
	}
	if err = db.Model(&nodes[3]).Updates(map[string]any{"ipv4": "", "ipv6": "2001:db8::3"}).Error; err != nil {
		t.Fatal(err)
	}
	config, err = service.Get(ctx)
	if err != nil || !slices.Equal(config.Addresses, []string{"2001:db8::3"}) {
		t.Fatalf("refreshed policy = %+v, err = %v", config, err)
	}
	if err = db.Delete(&nodes[3]).Error; err != nil {
		t.Fatal(err)
	}
	config, err = service.Get(ctx)
	if err != nil || !slices.Equal(config.NodeIDs, []uint{nodes[3].ID}) || len(config.Addresses) != 0 || !relayAdmission(config).Whitelist {
		t.Fatalf("policy after node deletion = %+v, err = %v", config, err)
	}
	if _, err = service.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}
	config, err = service.Get(ctx)
	if err != nil || config.NodeIDs == nil || len(config.NodeIDs) != 0 || config.Addresses == nil || len(config.Addresses) != 0 {
		t.Fatalf("cleared policy = %+v, err = %v", config, err)
	}
}
