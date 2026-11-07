package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"yagp/internal/config"
	"yagp/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAdminRouteBoundary(t *testing.T) {
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

	routes, err := NewRouter(db, config.AuthentikConfig{
		TrustedProxies: []string{"192.0.2.20"},
		AdminGroup:     "yagp-admins",
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	routes.Setup(engine)

	for _, tt := range []struct {
		method string
		path   string
		group  string
		want   int
	}{
		{http.MethodGet, "/", "", http.StatusUnauthorized},
		{http.MethodGet, "/", "staff", http.StatusForbidden},
		{http.MethodGet, "/", "yagp-admins", http.StatusOK},
		{http.MethodGet, "/api/v1/auth/info", "yagp-admins", http.StatusOK},
		{http.MethodGet, "/api/v1/nodes", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/system/public-config", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/health", "", http.StatusOK},
		{http.MethodPost, "/api/v1/auth/login", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/observer/report", "", http.StatusNotFound},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.RemoteAddr = "192.0.2.20:8100"
		if tt.group != "" {
			req.Header.Set("X-Authentik-Username", "alice")
			req.Header.Set("X-Authentik-Groups", tt.group)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s %s (%q) = %d, want %d", tt.method, tt.path, tt.group, rec.Code, tt.want)
		}
	}
}
