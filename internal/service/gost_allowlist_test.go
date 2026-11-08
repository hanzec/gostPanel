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
		{Name: "IPv4", Address: "192.0.2.1", Port: 39000},
		{Name: "IPv6", Address: "[2001:0db8::1]", Port: 39000},
		{Name: "hostname", Address: "example.com", Port: 39000},
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
		t.Fatal("accepted a node without a literal IP address")
	}
	config, err := service.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(config.NodeIDs, []uint{nodes[0].ID, nodes[1].ID}) ||
		!slices.Equal(config.Addresses, []string{"192.0.2.1", "2001:db8::1"}) {
		t.Fatalf("saved policy = %+v", config)
	}
	if _, err = service.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}
	config, err = service.Get(ctx)
	if err != nil || config.NodeIDs == nil || len(config.NodeIDs) != 0 || config.Addresses == nil || len(config.Addresses) != 0 {
		t.Fatalf("cleared policy = %+v, err = %v", config, err)
	}
}
