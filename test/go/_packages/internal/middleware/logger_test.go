package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"yagp/pkg/logger"

	"github.com/gin-gonic/gin"
)

func TestRequestLogLevels(t *testing.T) {
	requests := []struct {
		method, path string
		status       int
		level        string
	}{
		{http.MethodGet, "/health", http.StatusOK, "debug"},
		{http.MethodGet, "/cached", http.StatusNotModified, "debug"},
		{http.MethodGet, "/missing", http.StatusNotFound, "warn"},
		{http.MethodGet, "/unavailable", http.StatusServiceUnavailable, "warn"},
		{http.MethodPost, "/created", http.StatusCreated, "info"},
		{http.MethodPost, "/invalid", http.StatusBadRequest, "warn"},
	}
	router := gin.New()
	router.Use(Logger())
	for _, request := range requests {
		router.Handle(request.method, request.path, func(c *gin.Context) { c.Status(request.status) })
	}

	t.Cleanup(func() { _ = logger.Init(&logger.Config{Level: "error"}) })
	for _, threshold := range []string{"debug", "info"} {
		t.Run(threshold, func(t *testing.T) {
			logFile, err := os.CreateTemp(t.TempDir(), "requests-*.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			stdout := os.Stdout
			os.Stdout = logFile
			err = logger.Init(&logger.Config{Level: threshold})
			os.Stdout = stdout
			if err != nil {
				t.Fatal(err)
			}
			for _, request := range requests {
				router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(request.method, request.path, nil))
			}

			if _, err := logFile.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			logs := json.NewDecoder(logFile)
			for _, request := range requests {
				if threshold == "info" && request.level == "debug" {
					continue
				}
				var entry struct {
					Level   string `json:"level"`
					Message string `json:"msg"`
				}
				if err := logs.Decode(&entry); err != nil {
					t.Fatal(err)
				}
				if entry.Level != request.level || !strings.Contains(entry.Message, request.method+" "+request.path+" ") {
					t.Errorf("%s %s should log at %s, got %+v", request.method, request.path, request.level, entry)
				}
			}
			if err := logs.Decode(new(json.RawMessage)); err != io.EOF {
				t.Fatalf("unexpected extra request log, decode error = %v", err)
			}
		})
	}
}
