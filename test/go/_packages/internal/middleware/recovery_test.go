package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	business "yagp/internal/errors"
	"yagp/pkg/logger"

	"github.com/gin-gonic/gin"
)

func TestErrorMiddlewarePreservesResponsesAndRecoversPanics(t *testing.T) {
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(Logger(), Recovery(), ErrorHandler())
	engine.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ready") })
	engine.GET("/panic", func(*gin.Context) { panic("fixture panic") })
	engine.GET("/business", func(c *gin.Context) { _ = c.Error(business.New(6100, "port in use", http.StatusConflict)) })
	engine.GET("/unknown", func(c *gin.Context) { _ = c.Error(errors.New("unavailable")) })
	engine.GET("/written", func(c *gin.Context) {
		c.String(http.StatusAccepted, "queued")
		_ = c.Error(errors.New("after response"))
	})
	for _, test := range []struct {
		path         string
		status, code int
		message      string
	}{
		{"/ok", 200, 0, "ready"},
		{"/written", 202, 0, "queued"},
		{"/panic", 500, business.ErrInternal.Code, "fixture panic"},
		{"/business", 409, 6100, "port in use"},
		{"/unknown", 500, 50000, "unavailable"},
	} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
		if rec.Code != test.status {
			t.Fatalf("%s status = %d, want %d", test.path, rec.Code, test.status)
		}
		if test.code == 0 {
			if rec.Body.String() != test.message {
				t.Fatalf("%s body = %s", test.path, rec.Body)
			}
			continue
		}
		var body struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != test.code || !strings.Contains(body.Message, test.message) {
			t.Fatalf("%s body = %s, %v", test.path, rec.Body, err)
		}
	}
}
