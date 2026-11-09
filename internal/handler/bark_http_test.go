package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"yagp/internal/dto"
)

func TestSystemHTTPBarkConfig(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	const path = "/system/config"
	withBark := func(bark string) string {
		return strings.TrimSuffix(configuredSystemConfigPayload, "}") + `,"bark":` + bark + `}`
	}
	want := dto.BarkConfig{ServerURL: "https://bark.example/notifications", DeviceKey: "saved-device-key"}
	f.request(t, http.MethodPut, path, withBark(fmt.Sprintf(`{"serverUrl":%q,"deviceKey":%q}`, want.ServerURL, want.DeviceKey)), http.StatusOK)
	current := handlerData[dto.SystemConfigResp](t, f.request(t, http.MethodGet, path, "", http.StatusOK))
	if current.Bark != want || current.Email.Host != "mail.example" {
		t.Fatalf("saved notification settings: %+v", current)
	}

	public := f.request(t, http.MethodGet, "/system/public-config", "", http.StatusOK)
	if data := string(public.Data); strings.Contains(data, `"bark"`) || strings.Contains(data, want.ServerURL) || strings.Contains(data, want.DeviceKey) {
		t.Fatalf("public config exposes Bark settings: %s", public.Data)
	}

	f.request(t, http.MethodPut, path, configuredSystemConfigPayload, http.StatusOK)
	f.request(t, http.MethodPut, path, withBark(`{"serverUrl":"https://bark.example/notifications"}`), http.StatusBadRequest)
	f.request(t, http.MethodPut, path, withBark(`{"serverUrl":"https://bark.example/notifications","deviceKey":""}`), http.StatusBadRequest)
	f.request(t, http.MethodPut, path, withBark(`{"serverUrl":"ftp://bark.example/notifications","deviceKey":"another-key"}`), http.StatusBadRequest)
	current = handlerData[dto.SystemConfigResp](t, f.request(t, http.MethodGet, path, "", http.StatusOK))
	if current.Bark != want {
		t.Fatalf("Bark settings changed after legacy or invalid update: %+v", current.Bark)
	}

	f.request(t, http.MethodPut, path, withBark(`{"serverUrl":"","deviceKey":""}`), http.StatusOK)
	current = handlerData[dto.SystemConfigResp](t, f.request(t, http.MethodGet, path, "", http.StatusOK))
	if current.Bark != (dto.BarkConfig{}) || current.Email.Host != "mail.example" {
		t.Fatalf("clearing Bark changed notification settings unexpectedly: %+v", current)
	}
}

func TestSystemHTTPBarkPush(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerSettings()
	const path = "/system/bark/test"
	const deviceKey = "unsaved-device-key"
	var received, followedRedirect atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bark/push", func(w http.ResponseWriter, r *http.Request) {
		received.Store(true)
		var message struct {
			Title     string `json:"title"`
			Body      string `json:"body"`
			DeviceKey string `json:"device_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil || message.DeviceKey != deviceKey || message.Title == "" || message.Body == "" {
			t.Errorf("Bark push payload: %+v, %v", message, err)
		}
		if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("Bark push content type: %q", contentType)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"success"}`)
	})
	mux.HandleFunc("POST /rejected/push", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"code":400,"message":%q}`, deviceKey)
	})
	mux.HandleFunc("POST /redirect/push", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, _ *http.Request) {
		followedRedirect.Store(true)
		_, _ = io.WriteString(w, `{"code":200}`)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	f.request(t, http.MethodPost, path, `{"serverUrl":"","deviceKey":""}`, http.StatusBadRequest)
	f.request(t, http.MethodPost, path, fmt.Sprintf(`{"serverUrl":%q,"deviceKey":%q}`, upstream.URL+"/bark/", deviceKey), http.StatusOK)
	if !received.Load() {
		t.Fatal("the test push did not reach Bark")
	}
	current := handlerData[dto.SystemConfigResp](t, f.request(t, http.MethodGet, "/system/config", "", http.StatusOK))
	if current.Bark != (dto.BarkConfig{}) {
		t.Fatal("the test push saved Bark settings")
	}

	for _, endpoint := range []string{"/rejected", "/unavailable", "/redirect"} {
		body := fmt.Sprintf(`{"serverUrl":%q,"deviceKey":%q}`, upstream.URL+endpoint, deviceKey)
		failure := f.request(t, http.MethodPost, path, body, http.StatusBadGateway)
		if strings.Contains(failure.Message, deviceKey) || strings.Contains(failure.Message, upstream.URL) {
			t.Fatalf("push failure exposes Bark settings: %+v", failure)
		}
	}
	if followedRedirect.Load() {
		t.Fatal("the test push followed a Bark redirect")
	}
}
