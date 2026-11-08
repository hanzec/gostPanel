package service

import (
	"testing"

	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/pkg/logger"

	"github.com/gin-gonic/gin/binding"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestNodeTrafficLimitPreservesOmittedValueAndAcceptsZero(t *testing.T) {
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.GostNode{}, &model.OperationLog{}); err != nil {
		t.Fatal(err)
	}
	node := model.GostNode{
		Name: "node", Port: 39000, TotalBytes: 8192, Password: "fixture", TrafficLimitBytes: 4096,
		TrafficResetPeriod: "weekly", TrafficQuotaState: model.TrafficQuotaBlocked, TrafficQuotaUsedBytes: 4096,
		TrafficQuotaWindowStart: 1793548800,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	service := NewNodeService(db)
	for _, test := range []struct {
		body   string
		limit  int64
		period string
		state  string
	}{
		{`{"name":"node","port":39000}`, 4096, "weekly", model.TrafficQuotaBlocked},
		{`{"name":"node","port":39000,"traffic_reset_period":"daily"}`, 4096, "daily", model.TrafficQuotaPending},
		{`{"name":"node","port":39000,"traffic_limit_bytes":0}`, 0, "daily", model.TrafficQuotaPending},
	} {
		var request dto.UpdateNodeReq
		if err := binding.JSON.BindBody([]byte(test.body), &request); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Update(node.ID, &request, "admin", "127.0.0.1", "test"); err != nil {
			t.Fatal(err)
		}
		var saved model.GostNode
		if err := db.First(&saved, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.TrafficLimitBytes != test.limit || saved.TrafficResetPeriod != test.period || saved.TrafficQuotaState != test.state {
			t.Errorf("%s saved limit %d, period %q, state %q; want %d, %q, %q", test.body,
				saved.TrafficLimitBytes, saved.TrafficResetPeriod, saved.TrafficQuotaState, test.limit, test.period, test.state)
		}
		if saved.TotalBytes != node.TotalBytes || saved.TrafficQuotaUsedBytes != node.TrafficQuotaUsedBytes ||
			saved.TrafficQuotaWindowStart != node.TrafficQuotaWindowStart || saved.Password != node.Password {
			t.Error("updating the traffic limit changed usage, window or credentials")
		}
	}
}
