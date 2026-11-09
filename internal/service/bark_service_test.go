package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
)

func TestSendTestBarkHandlesUpstreamResponse(t *testing.T) {
	settings := &SystemConfigService{}
	const deviceKey = "test-device-key"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"accepted", http.StatusOK, `{"code":200}`, nil},
		{"HTTP failure", http.StatusBadGateway, `{"code":200}`, bizerrors.ErrBarkPushFailed},
		{"Bark rejection", http.StatusOK, `{"code":400}`, bizerrors.ErrBarkPushFailed},
		{"malformed response", http.StatusOK, `<html>unexpected response</html>`, bizerrors.ErrBarkPushFailed},
		{"oversize response", http.StatusOK, `{"code":200}` + strings.Repeat(" ", maxBarkResponseBytes), bizerrors.ErrBarkPushFailed},
		{"redirect", http.StatusTemporaryRedirect, "", bizerrors.ErrBarkPushFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					t.Error("Bark push followed an upstream redirect")
					_, _ = io.WriteString(w, `{"code":200}`)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/notifications/push" {
					t.Errorf("Bark request = %s %s", r.Method, r.URL.Path)
				}
				if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
					t.Errorf("Bark request content type = %q", contentType)
				}
				var message map[string]string
				if err := json.NewDecoder(r.Body).Decode(&message); err != nil || message["device_key"] != deviceKey ||
					message["title"] == "" || message["body"] == "" {
					t.Errorf("Bark request payload = %v, %v", message, err)
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			config := dto.BarkConfig{ServerURL: " " + upstream.URL + "/notifications/ ", DeviceKey: " " + deviceKey + " "}
			requireServiceError(t, settings.SendTestBark(t.Context(), config), tc.want)
		})
	}
}

func TestSendTestBarkCanceledRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("Bark received a canceled request")
		_, _ = io.WriteString(w, `{"code":200}`)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	settings := &SystemConfigService{}
	config := dto.BarkConfig{ServerURL: upstream.URL, DeviceKey: "test-device-key"}
	requireServiceError(t, settings.SendTestBark(ctx, config), bizerrors.ErrBarkPushFailed)
}

func TestSendTestBarkRejectsInvalidConfig(t *testing.T) {
	settings := &SystemConfigService{}
	const validURL = "https://bark.example.test"
	const validKey = "test-device-key"
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Validation should fail before any outbound request.
	for _, tc := range []struct {
		name, serverURL, deviceKey string
		want                       error
	}{
		{"missing server", "  ", validKey, bizerrors.ErrBarkConfigIncomplete},
		{"missing key", validURL, "  ", bizerrors.ErrBarkConfigIncomplete},
		{"long key", validURL, strings.Repeat("密", 86), bizerrors.ErrBarkDeviceKeyInvalid},
		{"malformed server URL", validURL + "/%zz", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"long server URL", validURL + "/" + strings.Repeat("x", 2048), validKey, bizerrors.ErrBarkServerURLInvalid},
		{"unsupported scheme", "ftp://bark.example.test", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"missing host", "https:///notifications", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"credentials", "https://user:password@bark.example.test", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"query", validURL + "?key=value", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"empty query", validURL + "?", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"empty fragment", validURL + "#", validKey, bizerrors.ErrBarkServerURLInvalid},
		{"out of range port", validURL + ":65536", validKey, bizerrors.ErrBarkServerURLInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := dto.BarkConfig{ServerURL: tc.serverURL, DeviceKey: tc.deviceKey}
			requireServiceError(t, settings.SendTestBark(ctx, config), tc.want)
		})
	}
}
