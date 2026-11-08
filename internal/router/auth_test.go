package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yagp/internal/config"
	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRouterRejectsMalformedTrustedProxy(t *testing.T) {
	if _, err := NewRouter(nil, config.AuthentikConfig{
		TrustedProxies: []string{"not-a-proxy"}, AdminGroup: "yagp-admins",
	}, nil); err == nil {
		t.Fatal("router started with an invalid trusted proxy")
	}
}

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
	if err := db.AutoMigrate(&model.GostNode{}, &model.OperationLog{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.GostNode{ID: 1, Name: "test", Address: "localhost", Port: 39000}).Error; err != nil {
		t.Fatal(err)
	}

	routes, err := NewRouter(db, config.AuthentikConfig{
		TrustedProxies: []string{"192.0.2.20"},
		AdminGroup:     "yagp-admins",
	}, service.NewNodeUpgradeService(db))
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
		{http.MethodGet, "/nodes/managed", "yagp-admins", http.StatusOK},
		{http.MethodGet, "/favicon.ico", "yagp-admins", http.StatusOK},
		{http.MethodGet, "/assets/unavailable.js", "yagp-admins", http.StatusNotFound},
		{http.MethodGet, "/api/v1/auth/info", "yagp-admins", http.StatusOK},
		{http.MethodGet, "/api/v1/nodes", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/nodes/1/gost/service", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/nodes/1/gost/logs", "staff", http.StatusForbidden},
		{http.MethodPost, "/api/v1/nodes/1/gost/restart", "staff", http.StatusForbidden},
		{http.MethodGet, "/api/v1/nodes/1/upgrade", "", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/nodes/1/upgrade", "staff", http.StatusForbidden},
		{http.MethodPost, "/api/v1/nodes/1/upgrade/gost", "staff", http.StatusForbidden},
		{http.MethodGet, "/api/v1/nodes/1/nft", "", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/nodes/1/nft", "staff", http.StatusForbidden},
		{http.MethodPost, "/api/v1/nodes/1/registration-token", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/nodes/1/registration-token", "staff", http.StatusForbidden},
		{http.MethodPost, "/api/v1/nodes/1/registration-token", "yagp-admins", http.StatusOK},
		{http.MethodGet, "/api/v1/system/public-config", "", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/system/gost-allowlist", "staff", http.StatusForbidden},
		{http.MethodPut, "/api/v1/system/gost-ruleset", "staff", http.StatusForbidden},
		{http.MethodGet, "/api/v1/health", "", http.StatusOK},
		{http.MethodGet, "/health", "", http.StatusOK},
		{http.MethodPost, "/api/v1/auth/login", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/observer/report", "", http.StatusNotFound},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.RemoteAddr = "192.0.2.20:8100"
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if tt.group != "" {
			req.Header.Set("X-Authentik-Username", "alice")
			req.Header.Set("X-Authentik-Groups", tt.group)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("%s %s (%q) = %d, want %d", tt.method, tt.path, tt.group, rec.Code, tt.want)
		}
		if tt.path == "/nodes/managed" && (rec.Code != http.StatusOK ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !bytes.Contains(rec.Body.Bytes(), []byte(`id="app"`))) {
			t.Fatal("a frontend deep link did not return the embedded app shell")
		}
		if tt.path == "/favicon.ico" && (rec.Body.Len() == 0 || strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html")) {
			t.Fatal("favicon route did not return the embedded asset")
		}
		if tt.path == "/api/v1/nodes/1/registration-token" && rec.Code == http.StatusOK {
			var reply struct {
				Data dto.NodeRegistrationToken `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			id, err := nodegrpc.ParseRegistrationTokenID(reply.Data.Token)
			if err != nil || id != 1 || reply.Data.ExpiresAt.IsZero() || rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("admin did not receive an uncached token and expiry for node 1")
			}
		}
	}
}
