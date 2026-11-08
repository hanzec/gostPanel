package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"yagp/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGostRulesetKeepsLastValidConversion(t *testing.T) {
	var payload atomic.Value
	payload.Store("payload:\n  - DOMAIN-SUFFIX,example.com\n")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, payload.Load().(string))
	}))
	defer upstream.Close()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "rules.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.GostRuleset{}); err != nil {
		t.Fatal(err)
	}
	service := NewGostRulesetService(db)
	service.client = upstream.Client()
	ctx := context.Background()
	configured, err := service.Update(ctx, upstream.URL, nil)
	if err != nil || !slices.Equal(configured.Matchers, []string{".example.com"}) || configured.UpdatedAt == nil {
		t.Fatalf("initial conversion = %+v, %v", configured, err)
	}

	payload.Store("payload:\n  - RULE-SET,unsupported\n")
	if _, err = service.Update(ctx, upstream.URL, nil); err == nil {
		t.Fatal("an unsupported rule set was accepted")
	}
	stored, err := service.Get(ctx)
	if err != nil || !slices.Equal(stored.Matchers, configured.Matchers) {
		t.Fatalf("invalid subscription replaced the last valid matchers: %+v, %v", stored, err)
	}

	payload.Store("payload:\n  - IP-CIDR6,2001:db8::/32,no-resolve\n")
	service.refresh(ctx)
	stored, err = service.Get(ctx)
	if err != nil || !slices.Equal(stored.Matchers, []string{"2001:db8::/32"}) {
		t.Fatalf("refreshed matchers = %+v, %v", stored, err)
	}
}
