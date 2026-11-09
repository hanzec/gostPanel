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

func TestGostRulesetFollowsSecureRedirectAndKeepsRulesOnTruncation(t *testing.T) {
	var truncate atomic.Bool
	var forwardedReferrer atomic.Value
	forwardedReferrer.Store("")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subscribe":
			http.Redirect(w, r, "/payload", http.StatusFound)
		case "/payload":
			forwardedReferrer.Store(r.Referer())
			if truncate.Load() {
				w.Header().Set("Content-Length", "256")
			}
			_, _ = fmt.Fprint(w, "payload:\n  - DOMAIN-SUFFIX,secure.example\n")
		case "/downgrade":
			http.Redirect(w, r, "http://"+r.Host+"/payload", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	db := newServiceTestDB(t)
	service := NewGostRulesetService(db)
	service.client.Transport = upstream.Client().Transport

	saved, err := service.Update(t.Context(), upstream.URL+"/subscribe?credential=private", nil)
	requireServiceError(t, err, nil)
	if !slices.Equal(saved.Matchers, []string{".secure.example"}) || forwardedReferrer.Load().(string) != "" {
		t.Fatal("secure redirect did not preserve the rules or sent the previous URL as a referrer")
	}
	if _, err := service.Update(t.Context(), upstream.URL+"/downgrade", nil); err == nil {
		t.Fatal("ruleset update accepted a redirect to HTTP")
	}

	truncate.Store(true)
	service.refresh(t.Context())
	current, err := service.Get(t.Context())
	requireServiceError(t, err, nil)
	if current.URL != saved.URL || !slices.Equal(current.Matchers, saved.Matchers) || !current.UpdatedAt.Equal(*saved.UpdatedAt) {
		t.Fatal("a truncated download replaced a working ruleset")
	}
	truncate.Store(false)
	requireServiceError(t, db.Exec(`CREATE TRIGGER fail_ruleset_save BEFORE UPDATE ON gost_rulesets
		BEGIN SELECT RAISE(ABORT, 'ruleset storage unavailable'); END`).Error, nil)
	if _, err := service.Update(t.Context(), upstream.URL+"/payload", nil); err == nil {
		t.Fatal("ruleset update reported success while storage rejected it")
	}
	service.refresh(t.Context())
	current, err = service.Get(t.Context())
	requireServiceError(t, err, nil)
	if current.URL != saved.URL || !current.UpdatedAt.Equal(*saved.UpdatedAt) {
		t.Fatal("a failed write advanced the saved subscription")
	}
	service.sync(t.Context(), map[uint]bool{})
}
