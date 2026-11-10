package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"yagp/internal/deployjob"
)

func TestSignedUpdateStatusSurvivesReceiverRestart(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	token := []byte(strings.Repeat("b", 64))
	toolSecret := []byte(strings.Repeat("c", 64))
	toolToken := []byte(strings.Repeat("d", 64))
	jobs := make(chan deployjob.Request, 1)
	handler := testHandler(t, secret, token, jobs)
	handler.upgrade = testHook(t, upgradeProtocol, toolSecret, toolToken)
	handler.directory = t.TempDir()

	digest := "sha256:" + strings.Repeat("e", 64)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "https://untrusted.example"+deployPath, strings.NewReader(digest))
	request.RemoteAddr = "172.18.0.2:12345"
	request.Header.Set("X-Real-IP", "192.0.2.10")
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("X-Yagp-Timestamp", timestamp)
	request.Header.Set("X-Yagp-Signature", signRequest(secret, timestamp, digest))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var accepted struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); response.Code != http.StatusAccepted || err != nil {
		t.Fatalf("acceptance response %d: %q (%v)", response.Code, response.Body.String(), err)
	}
	link, err := url.Parse(accepted.StatusURL)
	if err != nil || link.IsAbs() || link.Host != "" || !strings.HasPrefix(link.Path, statusPath) ||
		accepted.StatusURL != response.Header().Get("Location") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid acceptance link: %q, headers %v, parse error %v", accepted.StatusURL, response.Header(), err)
	}
	var job deployjob.Request
	select {
	case job = <-jobs:
	default:
		t.Fatal("accepted request did not submit a job")
	}
	if link.Path != statusPath+job.ID {
		t.Fatalf("link path %q does not reference submitted job %+v", link.Path, job)
	}
	jobDir := filepath.Join(handler.directory, job.ID)
	if err := os.Mkdir(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeWebhookFixture(t, filepath.Join(jobDir, "status.json"), `{"mode":"compose","state":"running"}`)
	writeWebhookFixture(t, filepath.Join(jobDir, "job.log"), "deploying image\n")
	read := func(path, peer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = peer
		req.Header.Set("X-Real-IP", "192.0.2.10")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	assertStatus := func(resp *httptest.ResponseRecorder, state, logs string) deployjob.Status {
		t.Helper()
		var status deployjob.Status
		err := json.Unmarshal(resp.Body.Bytes(), &status)
		if resp.Code != http.StatusOK || resp.Header().Get("Cache-Control") != "no-store" ||
			err != nil || status.Mode != job.Mode || status.State != state || status.Logs != logs {
			t.Fatalf("status response %d: %q (%v)", resp.Code, resp.Body.String(), err)
		}
		return status
	}
	const proxy = "172.18.0.2:12345"
	assertStatus(read(accepted.StatusURL, proxy), "running", "deploying image\n")

	handler.limits = &requestLimits{clients: make(map[netip.Addr]*clientLimits)}
	writeWebhookFixture(t, filepath.Join(jobDir, "status.json"), `{"mode":"compose","state":"succeeded","exit_code":0}`)
	writeWebhookFixture(t, filepath.Join(jobDir, "job.log"), "deployment completed\n")
	status := assertStatus(read(accepted.StatusURL, proxy), "succeeded", "deployment completed\n")
	if status.ExitCode == nil || *status.ExitCode != 0 {
		t.Fatalf("terminal status lacks successful exit code: %+v", status)
	}

	if resp := read(accepted.StatusURL, "192.0.2.99:12345"); resp.Code != http.StatusForbidden {
		t.Fatalf("untrusted status source returned %d", resp.Code)
	}
	for _, invalid := range []string{
		strings.Replace(accepted.StatusURL, job.ID, strings.Repeat("f", 32), 1),
		strings.Replace(accepted.StatusURL, "mode=compose", "mode=depoly", 1),
		accepted.StatusURL + "&mode=compose",
		signedStatusURL(secret, job, time.Now().Add(-time.Second)),
	} {
		if resp := read(invalid, proxy); resp.Code != http.StatusUnauthorized {
			t.Fatalf("invalid status link returned %d", resp.Code)
		}
	}
	unknown := deployjob.Request{ID: strings.Repeat("f", 32), Mode: "compose"}
	if resp := read(signedStatusURL(secret, unknown, time.Now().Add(time.Minute)), proxy); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown update job returned %d", resp.Code)
	}
	wrongMode := deployjob.Request{ID: job.ID, Mode: "depoly"}
	if resp := read(signedStatusURL(toolSecret, wrongMode, time.Now().Add(time.Minute)), proxy); resp.Code != http.StatusNotFound {
		t.Fatalf("update job with mismatched mode returned %d", resp.Code)
	}
	writeWebhookFixture(t, filepath.Join(jobDir, "status.json"), `{"mode":"compose",`)
	if resp := read(accepted.StatusURL, proxy); resp.Code != http.StatusInternalServerError || strings.Contains(resp.Body.String(), "deploying") {
		t.Fatalf("corrupt update status returned %d: %s", resp.Code, resp.Body.String())
	}
}
