package nodebridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/proto"
)

func TestRefreshRetriesSamePersistedRequestAfterRestart(t *testing.T) {
	configPath := preparePanelCredentials(t)
	path := filepath.Join(filepath.Dir(configPath), PanelCredentialsFile)
	now := time.Now()
	old := panelCredentials{
		AccessToken: "expired", RefreshToken: "still-valid",
		AccessExpiresAt: now.Add(30 * time.Second).Unix(), RefreshExpiresAt: now.Add(time.Hour).Unix(),
	}
	if err := writePanelCredentials(path, old); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	requests := make(chan string, 2)
	newCredentials := testCredentials(now.Add(15*time.Minute), "rotated")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := new(nodegrpc.RefreshNodeRequest)
		data, err := io.ReadAll(r.Body)
		if r.URL.Path != nodegrpc.RefreshPath || r.Method != http.MethodPost || err != nil || proto.Unmarshal(data, request) != nil ||
			request.GetNodeId() != 42 || request.GetRefreshToken() != old.RefreshToken || len(request.GetRequestId()) != 32 {
			t.Error("invalid refresh request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stored, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(stored), "pending_refresh_id: "+request.GetRequestId()) {
			t.Error("refresh request reached the server before its ID was persisted")
		}
		requests <- request.GetRequestId()
		if calls.Add(1) == 1 {
			// Commit at the fixture, then close the connection before sending the response.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		writeTestProtobuf(t, w, newCredentials)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Transport.(*http.Transport).DialContext = dialPanelAt(server.Listener)
	host, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{NodeID: 42, ConfigFile: configPath}
	manager, err := openCredentialManager(cfg, host, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err = manager.accessToken(ctx, refreshLeadTime); err == nil {
		t.Fatal("lost refresh response unexpectedly succeeded")
	}
	manager, err = openCredentialManager(cfg, host, client)
	if err != nil {
		t.Fatal(err)
	}
	if delay := manager.nextRefresh(); delay != 0 {
		t.Fatalf("pending rotation is not retried immediately: %v", delay)
	}
	if token, err := manager.accessToken(ctx, 0); err != nil || token != "rotated" {
		t.Fatalf("retry failed: %v", err)
	}
	if calls.Load() != 2 || <-requests != <-requests {
		t.Fatalf("refresh retry used a different request ID or had an unexpected count: %d", calls.Load())
	}
	manager, err = openCredentialManager(cfg, host, client)
	if err != nil || manager.credentials.RefreshToken != newCredentials.GetRefreshToken() || manager.credentials.PendingRefreshID != "" {
		t.Fatalf("rotation was not saved: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("rotated credentials are not private: %v, %v", info, err)
	}
}

func TestCredentialManagerRejectsInvalidAndExpiredState(t *testing.T) {
	configPath := preparePanelCredentials(t)
	path := filepath.Join(filepath.Dir(configPath), PanelCredentialsFile)
	cfg := Config{NodeID: 42, ConfigFile: configPath}
	if _, err := EncodePanelCredentials(nil); err == nil {
		t.Fatal("empty credentials encoded")
	}
	if err := os.WriteFile(path, []byte("["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openCredentialManager(cfg, nodegrpc.PanelHost{}, nil); err == nil || !strings.Contains(err.Error(), "invalid stored") {
		t.Fatalf("invalid state: %v", err)
	}
	now := time.Now()
	if err := writePanelCredentials(path, panelCredentials{
		AccessToken: "old", RefreshToken: "also-old",
		AccessExpiresAt: now.Add(-time.Hour).Unix(), RefreshExpiresAt: now.Add(-time.Minute).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := openCredentialManager(cfg, nodegrpc.PanelHost{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.accessToken(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "re-enroll") {
		t.Fatalf("expired state: %v", err)
	}
	if err := WriteNodeFile(filepath.Join(configPath, "missing"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write missing directory: %v", err)
	}
	if err := WriteNodeFile(filepath.Dir(path), nil); err == nil {
		t.Fatal("replacing a directory succeeded")
	}
}

func TestCredentialRefreshRejectsRevocationAndRequiresPersistence(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Transport.(*http.Transport).DialContext = dialPanelAt(server.Listener)
	host, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	configPath := preparePanelCredentials(t)
	manager, err := openCredentialManager(Config{NodeID: 42, ConfigFile: configPath}, host, client)
	if err != nil {
		t.Fatal(err)
	}
	manager.credentials.AccessExpiresAt = time.Now().Add(-time.Second).Unix()
	path := manager.path
	manager.path = filepath.Join(configPath, "missing")
	if _, err := manager.accessToken(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "persist refresh request") {
		t.Fatalf("refresh without persistent storage: %v", err)
	}
	if manager.credentials.PendingRefreshID != "" {
		t.Fatal("a request ID was used without first saving it")
	}
	manager.path = path
	if _, err := manager.accessToken(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "re-enroll the node") {
		t.Fatalf("revoked refresh token: %v", err)
	}
}
