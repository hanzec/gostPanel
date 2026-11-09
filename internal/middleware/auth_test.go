package middleware

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"yagp/internal/config"
	"yagp/pkg/logger"

	"github.com/gin-gonic/gin"
)

func TestAuthentikAuth(t *testing.T) {
	auth, err := NewAuthentikAuth(config.AuthentikConfig{
		TrustedProxies: []string{"192.0.2.20"},
		AdminGroup:     "yagp-admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Any("/", auth, func(c *gin.Context) {
		c.String(http.StatusOK, "%s", c.GetString("username"))
	})

	tests := []struct {
		name   string
		method string
		edit   func(*http.Request)
		want   int
	}{
		{name: "trusted administrator", method: http.MethodGet, want: http.StatusOK},
		{
			name: "missing peer port", method: http.MethodGet,
			edit: func(req *http.Request) { req.RemoteAddr = "192.0.2.20" }, want: http.StatusUnauthorized,
		},
		{
			name: "invalid peer address", method: http.MethodGet,
			edit: func(req *http.Request) { req.RemoteAddr = "panel.example:8100" }, want: http.StatusUnauthorized,
		},
		{
			name:   "untrusted direct connection with spoofed headers",
			method: http.MethodGet,
			edit: func(req *http.Request) {
				req.RemoteAddr = "198.51.100.20:8100"
				req.Header.Set("X-Forwarded-For", "192.0.2.20")
			},
			want: http.StatusUnauthorized,
		},
		{
			name:   "trusted peer without identity",
			method: http.MethodGet,
			edit:   func(req *http.Request) { req.Header.Del("X-Authentik-Username") },
			want:   http.StatusUnauthorized,
		},
		{
			name:   "ambiguous identity",
			method: http.MethodGet,
			edit:   func(req *http.Request) { req.Header.Add("X-Authentik-Username", "mallory") },
			want:   http.StatusUnauthorized,
		},
		{
			name:   "different group with shared prefix",
			method: http.MethodGet,
			edit:   func(req *http.Request) { req.Header.Set("X-Authentik-Groups", "yagp-admin-readonly") },
			want:   http.StatusForbidden,
		},
		{
			name:   "same origin mutation behind TLS proxy",
			method: http.MethodPost,
			edit: func(req *http.Request) {
				req.Header.Set("Origin", "https://panel.example")
				req.Header.Set("X-Forwarded-Proto", "https")
			},
			want: http.StatusOK,
		},
		{
			name:   "browser same origin metadata",
			method: http.MethodDelete,
			edit:   func(req *http.Request) { req.Header.Set("Sec-Fetch-Site", "same-origin") },
			want:   http.StatusOK,
		},
		{
			name:   "same origin mutation over direct TLS",
			method: http.MethodPost,
			edit: func(req *http.Request) {
				req.Header.Set("Origin", "https://panel.example")
				req.TLS = &tls.ConnectionState{}
			},
			want: http.StatusOK,
		},
		{
			name:   "ambiguous mutation origin",
			method: http.MethodPost,
			edit: func(req *http.Request) {
				req.Header.Add("Origin", "http://panel.example")
				req.Header.Add("Origin", "http://untrusted.example")
			},
			want: http.StatusForbidden,
		},
		{
			name:   "cross origin mutation",
			method: http.MethodPost,
			edit:   func(req *http.Request) { req.Header.Set("Origin", "http://untrusted.example") },
			want:   http.StatusForbidden,
		},
		{
			name:   "same-site cross-origin mutation",
			method: http.MethodPut,
			edit:   func(req *http.Request) { req.Header.Set("Sec-Fetch-Site", "same-site") },
			want:   http.StatusForbidden,
		},
		{name: "missing mutation origin", method: http.MethodPost, want: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://panel.example/", nil)
			req.RemoteAddr = "192.0.2.20:8100"
			req.Header.Set("X-Authentik-Username", "alice")
			req.Header.Set("X-Authentik-Groups", "staff|yagp-admin")
			if tt.edit != nil {
				tt.edit(req)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusOK && rec.Body.String() != "alice" {
				t.Errorf("authenticated username = %q, want alice", rec.Body.String())
			}
		})
	}
}

func TestAuthentikPageReturnsAndLogsTheDenialReason(t *testing.T) {
	logFile, err := os.CreateTemp(t.TempDir(), "auth-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	stdout := os.Stdout
	os.Stdout = logFile
	err = logger.Init(&logger.Config{Level: "warn"})
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Init(&logger.Config{Level: "error"}) })

	auth, err := NewAuthentikAuth(config.AuthentikConfig{
		TrustedProxies: []string{"192.0.2.20"}, AdminGroup: "yagp-admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(Logger())
	router.Any("/", MarkAuthentikPage, auth, func(c *gin.Context) { c.String(http.StatusOK, "admin panel") })
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "https://panel.example/", nil)
		req.RemoteAddr = "192.0.2.20:8100"
		req.Header.Set("X-Authentik-Username", "alice")
		req.Header.Set("X-Authentik-Groups", "staff|yagp-admin-readonly")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
			rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s denial status = %d, headers = %v", method, rec.Code, rec.Header())
		}
		if method == http.MethodHead {
			if rec.Body.Len() != 0 {
				t.Fatal("HEAD denial returned a response body")
			}
		} else if !strings.Contains(rec.Body.String(), "admin_group_required") || strings.Contains(rec.Body.String(), "yagp-admin-readonly") {
			t.Fatal("denial page omitted the diagnosis or exposed the received group names")
		}
	}

	logs, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(logs)))
	for range 2 {
		var log struct {
			Failure string   `json:"auth_failure"`
			Remote  string   `json:"remote_addr"`
			Groups  []string `json:"authentik_groups"`
		}
		if err := decoder.Decode(&log); err != nil || log.Failure != "admin_group_required" || log.Remote != "192.0.2.20:8100" ||
			!slices.Equal(log.Groups, []string{"staff", "yagp-admin-readonly"}) {
			t.Fatalf("denial log = %+v, %v", log, err)
		}
	}
}

func TestAuthentikAuthRejectsUnsafeConfiguration(t *testing.T) {
	for _, cfg := range []config.AuthentikConfig{
		{AdminGroup: "yagp-admin"},
		{TrustedProxies: []string{"192.0.2.20"}},
		{AdminGroup: "yagp-admin", TrustedProxies: []string{"0.0.0.0/0"}},
	} {
		if _, err := NewAuthentikAuth(cfg); err == nil {
			t.Errorf("expected invalid configuration to fail: %+v", cfg)
		}
	}
}
