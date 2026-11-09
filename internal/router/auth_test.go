package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"yagp/internal/config"
	"yagp/internal/dto"
	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRouterRejectsMalformedTrustedProxy(t *testing.T) {
	if _, err := NewRouter(nil, config.AuthentikConfig{
		TrustedProxies: []string{"not-a-proxy"}, AdminGroup: "yagp-admins",
	}, nil, ""); err == nil {
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

	fingerprint := strings.Repeat("ab", 32)
	routes, err := NewRouter(db, config.AuthentikConfig{
		TrustedProxies: []string{"192.0.2.20"},
		AdminGroup:     "yagp-admins",
	}, service.NewNodeUpgradeService(db), fingerprint)
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
		{http.MethodGet, "/favicon.svg", "", http.StatusOK},
		{http.MethodHead, "/favicon.svg", "", http.StatusOK},
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
		{http.MethodPost, "/api/v1/rules/1/listener-check", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/rules/1/listener-check", "staff", http.StatusForbidden},
		{http.MethodPost, "/api/v1/nodes/1/registration-token", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/nodes/1/registration-token", "staff", http.StatusForbidden},
		{http.MethodPost, "/api/v1/nodes/1/registration-token", "yagp-admins", http.StatusOK},
		{http.MethodDelete, "/api/v1/nodes/1/credentials", "", http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/nodes/1/credentials", "staff", http.StatusForbidden},
		{http.MethodDelete, "/api/v1/nodes/0/credentials", "yagp-admins", http.StatusBadRequest},
		{http.MethodDelete, "/api/v1/nodes/99/credentials", "yagp-admins", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/nodes/1/credentials", "yagp-admins", http.StatusOK},
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
		if tt.path == "/favicon.svg" {
			if rec.Header().Get("Content-Type") != "image/svg+xml" {
				t.Fatalf("favicon content type = %q", rec.Header().Get("Content-Type"))
			}
			if tt.method == http.MethodGet && !bytes.Contains(rec.Body.Bytes(), []byte("<svg")) {
				t.Fatal("GET favicon did not return the SVG asset")
			}
			if tt.method == http.MethodHead && rec.Body.Len() != 0 {
				t.Fatal("HEAD favicon returned a body")
			}
		}
		if tt.path == "/api/v1/nodes/1/registration-token" && rec.Code == http.StatusOK {
			var reply struct {
				Data dto.NodeRegistrationToken `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			id, pin, err := nodegrpc.ParseRegistrationToken(reply.Data.Token)
			if err != nil || id != 1 || pin != fingerprint || reply.Data.ExpiresAt.IsZero() || rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("admin did not receive an uncached token, certificate fingerprint, and expiry for node 1")
			}
		}
	}

	t.Run("authentication error formats", func(t *testing.T) {
		testAuthentikFailureResponses(t, engine)
	})
}

func testAuthentikFailureResponses(t *testing.T, engine *gin.Engine) {
	t.Helper()
	logFile, err := os.CreateTemp(t.TempDir(), "access-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = logger.Init(&logger.Config{Level: "error"})
		_ = logFile.Close()
	})
	stdout := os.Stdout
	os.Stdout = logFile
	err = logger.Init(&logger.Config{Level: "warn"})
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}

	const (
		trustedProxy   = "192.0.2.20:8100"
		untrustedProxy = "198.51.100.50:8100"
		username       = "private-authentik-username"
		staffGroup     = "private-authentik-staff"
		readOnlyGroup  = "private-authentik-read-only"
		groups         = staffGroup + "|" + readOnlyGroup
	)
	tests := []struct {
		name   string
		method string
		path   string
		peer   string
		user   string
		groups string
		status int
		reason string
		html   bool
	}{
		{"homepage with an untrusted proxy", http.MethodGet, "/", untrustedProxy, username, groups, http.StatusUnauthorized, "untrusted_proxy", true},
		{"HEAD homepage with an untrusted proxy", http.MethodHead, "/", untrustedProxy, username, groups, http.StatusUnauthorized, "untrusted_proxy", true},
		{"deep link without identity", http.MethodGet, "/nodes/managed", trustedProxy, "", groups, http.StatusUnauthorized, "invalid_identity", true},
		{"homepage without administrator access", http.MethodGet, "/", trustedProxy, username, groups, http.StatusForbidden, "admin_group_required", true},
		{"homepage without a groups header", http.MethodGet, "/", trustedProxy, username, "", http.StatusForbidden, "admin_group_required", true},
		{"API with browser accept header", http.MethodGet, "/api/v1/auth/info", trustedProxy, "", groups, http.StatusUnauthorized, "invalid_identity", false},
		{"asset with browser accept header", http.MethodGet, "/assets/unavailable.js", trustedProxy, "", groups, http.StatusUnauthorized, "invalid_identity", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.RemoteAddr = tt.peer
			req.Header.Set("Accept", "text/html")
			req.Header.Set("X-Forwarded-For", "203.0.113.60")
			if tt.groups != "" {
				req.Header.Set("X-Authentik-Groups", tt.groups)
			}
			if tt.user != "" {
				req.Header.Set("X-Authentik-Username", tt.user)
			}
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			if rec.Code != tt.status || rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status = %d, headers = %v, body = %s", rec.Code, rec.Header(), rec.Body)
			}

			contentType := rec.Header().Get("Content-Type")
			if !tt.html {
				var reply response.Response
				if !strings.HasPrefix(contentType, "application/json") || json.Unmarshal(rec.Body.Bytes(), &reply) != nil ||
					reply.Code != response.CodeUnauthorized || reply.Message != "请通过 Authentik 登录" || reply.Data != nil {
					t.Fatalf("expected the existing JSON auth error, got %s: %s", contentType, rec.Body)
				}
				return
			}

			if !strings.HasPrefix(contentType, "text/html") {
				t.Fatalf("expected HTML, got %s: %s", contentType, rec.Body)
			}
			if tt.method == http.MethodHead {
				if rec.Body.Len() != 0 {
					t.Fatalf("expected an empty HEAD response, got %d bytes", rec.Body.Len())
				}
				return
			}

			body := rec.Body.String()
			if !strings.Contains(body, tt.reason) {
				t.Fatalf("expected HTML with reason %q, got %s", tt.reason, body)
			}
			for _, private := range []string{`id="app"`, username, staffGroup, readOnlyGroup, "yagp-admins", "192.0.2.20", "198.51.100.50", "203.0.113.60"} {
				if strings.Contains(body, private) {
					t.Errorf("error page disclosed %q", private)
				}
			}
		})
	}

	data, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(tests) {
		t.Fatalf("expected one warning per auth failure, got %d: %s", len(lines), data)
	}
	for i, line := range lines {
		tt := tests[i]
		var record struct {
			AuthFailure     string   `json:"auth_failure"`
			RemoteAddr      string   `json:"remote_addr"`
			AuthentikGroups []string `json:"authentik_groups"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil || record.AuthFailure != tt.reason || record.RemoteAddr != tt.peer {
			t.Errorf("access log for %s: %s", tt.name, line)
		}
		if strings.Contains(line, username) || strings.Contains(line, groups) {
			t.Errorf("access log for %s disclosed raw identity headers", tt.name)
		}
		if tt.reason == "admin_group_required" {
			want := []string{}
			if tt.groups != "" {
				want = []string{staffGroup, readOnlyGroup}
			}
			if record.AuthentikGroups == nil || !slices.Equal(record.AuthentikGroups, want) {
				t.Errorf("access log for %s has groups %v, want %v", tt.name, record.AuthentikGroups, want)
			}
		} else if strings.Contains(line, `"authentik_groups"`) || strings.Contains(line, staffGroup) || strings.Contains(line, readOnlyGroup) {
			t.Errorf("access log for %s disclosed groups: %s", tt.name, line)
		}
	}
}
