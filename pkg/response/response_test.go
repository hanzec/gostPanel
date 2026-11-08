package response

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	business "yagp/internal/errors"
	"yagp/pkg/logger"

	"github.com/gin-gonic/gin"
)

func TestHTTPResponseContracts(t *testing.T) {
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		write      func(*gin.Context)
		http, code int
		message    string
		data       string
	}{
		{"success", func(c *gin.Context) { Success(c, "ready") }, 200, 0, MsgSuccess, `"ready"`},
		{"custom success", func(c *gin.Context) { SuccessWithMessage(c, "updated", 42) }, 200, 0, "updated", "42"},
		{"page", func(c *gin.Context) { SuccessPage(c, []string{"node"}, 20, 2, 5) }, 200, 0, MsgSuccess, `{"list":["node"],"total":20,"page":2,"pageSize":5}`},
		{"bad request", func(c *gin.Context) { BadRequest(c, "") }, 400, CodeBadRequest, MsgBadRequest, "null"},
		{"unauthorized", func(c *gin.Context) { Unauthorized(c, "") }, 401, CodeUnauthorized, MsgUnauthorized, "null"},
		{"forbidden", func(c *gin.Context) { Forbidden(c, "") }, 403, CodeForbidden, MsgForbidden, "null"},
		{"not found", func(c *gin.Context) { NotFound(c, "") }, 404, CodeNotFound, MsgNotFound, "null"},
		{"internal", func(c *gin.Context) { InternalError(c, "") }, 500, CodeInternalError, MsgInternalError, "null"},
		{"business error", func(c *gin.Context) { HandleError(c, business.New(6001, "port in use", 409)) }, 409, 6001, "port in use", "null"},
		{"unknown error", func(c *gin.Context) { HandleError(c, errors.New("unavailable")) }, 500, CodeInternalError, "unavailable", "null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			HandleError(c, nil)
			if c.Writer.Written() {
				t.Fatal("nil error wrote a response")
			}
			test.write(c)
			var response struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			}
			err := json.Unmarshal(rec.Body.Bytes(), &response)
			if err != nil || rec.Code != test.http || response.Code != test.code ||
				response.Message != test.message || string(response.Data) != test.data {
				t.Fatalf("HTTP %d body %s, decode error %v", rec.Code, rec.Body, err)
			}
			if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("content type = %q, want JSON", rec.Header().Get("Content-Type"))
			}
		})
	}
}
