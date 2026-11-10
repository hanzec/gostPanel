package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"yagp/internal/deployjob"
)

func TestServeRejectsInvalidConfiguration(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	token := filepath.Join(dir, "token")
	state := filepath.Join(dir, "state")
	writeWebhookFixture(t, secret, strings.Repeat("a", 64))
	writeWebhookFixture(t, token, strings.Repeat("b", 64))
	writeWebhookFixture(t, state, "bad state")
	validArgs := webhookArgs("127.0.0.1:0", secret, token, state+".new")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"-bogus"}, "flag provided but not defined"},
		{"missing flags", nil, "set -listen"},
		{"missing secret", webhookArgs("127.0.0.1:0", secret+".missing", token, state), "webhook signing secret"},
		{"missing token", webhookArgs("127.0.0.1:0", secret, token+".missing", state), "webhook token"},
		{"identical credentials", webhookArgs("127.0.0.1:0", secret, secret, state), "must differ"},
		{"corrupt state", webhookArgs("127.0.0.1:0", secret, token, state), "load webhook acceptance state"},
		{"incomplete upgrade flags", append(validArgs, "-upgrade-token-file", token), "set all of -upgrade-secret-file"},
		{"shared acceptance state", append(validArgs, upgradeWebhookArgs(secret, token, state+".new")...), "state files must differ"},
		{"shared credentials", append(validArgs, upgradeWebhookArgs(secret, token, state+".upgrade")...), "credentials must differ"},
		{"missing upgrade token", append(validArgs, upgradeWebhookArgs(secret, token+".missing", state+".upgrade")...), "tool upgrade webhook: webhook token"},
		{"invalid listen", webhookArgs("127.0.0.1:-1", secret, token, state+".new"), "listen for deployment webhooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := serveWebhook(tc.args, (*http.Server).Serve)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("serve error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestServeWebhookOverTCP(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	token := filepath.Join(dir, "token")
	upgradeSecret := filepath.Join(dir, "upgrade.secret")
	upgradeToken := filepath.Join(dir, "upgrade.token")
	writeWebhookFixture(t, secret, strings.Repeat("a", 64))
	writeWebhookFixture(t, token, strings.Repeat("b", 64))
	writeWebhookFixture(t, upgradeSecret, strings.Repeat("c", 64))
	writeWebhookFixture(t, upgradeToken, strings.Repeat("d", 64))
	notify, socket := listenNotifySocket(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	args := append(webhookArgs("127.0.0.1:0", secret, token, filepath.Join(dir, "state")),
		upgradeWebhookArgs(upgradeSecret, upgradeToken, filepath.Join(dir, "upgrade.state"))...)
	err := serveWebhook(args,
		func(server *http.Server, listener net.Listener) error {
			assertReadyMessage(t, notify)
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			defer func() {
				server.Close()
				if err := <-done; !errors.Is(err, http.ErrServerClosed) {
					t.Errorf("HTTP server shutdown = %v", err)
				}
			}()
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
			defer client.CloseIdleConnections()
			for _, path := range []string{"/api/deploy/panel", "/api/deploy/deploy-tool"} {
				request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("X-Real-IP", "192.0.2.10")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("real HTTP request to %s without credentials = %d", path, response.StatusCode)
				}
			}
			return nil
		})
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("loopback TCP unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestNotifyReady(t *testing.T) {
	if err := notifyReady(""); err != nil {
		t.Fatalf("notify without systemd = %v", err)
	}
	if err := notifyReady(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("notify accepted a missing systemd socket")
	}
	if runtime.GOOS == "linux" {
		conn, socket := listenNotifySocket(t, true)
		if err := notifyReady(socket); err != nil {
			t.Fatal(err)
		}
		assertReadyMessage(t, conn)
	}
}

func listenNotifySocket(t *testing.T, abstract bool) (*net.UnixConn, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix datagram unavailable on Windows")
	}
	dir, err := os.MkdirTemp("/tmp", "yagp-notify-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	name := filepath.Join(dir, "socket")
	if abstract {
		name = "@" + filepath.Base(dir)
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if errors.Is(err, os.ErrPermission) {
		t.Skipf("Unix datagram unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, name
}

func assertReadyMessage(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var message [32]byte
	n, _, err := conn.ReadFromUnix(message[:])
	if err != nil || string(message[:n]) != "READY=1" {
		t.Fatalf("systemd notification = %q, %v", message[:n], err)
	}
}

func webhookArgs(listen, secret, token, state string) []string {
	return []string{"-listen", listen, "-secret-file", secret, "-token-file", token, "-trusted-proxy", "127.0.0.1", "-state-file", state}
}

func upgradeWebhookArgs(secret, token, state string) []string {
	return []string{"-upgrade-secret-file", secret, "-upgrade-token-file", token, "-upgrade-state-file", state}
}

func writeWebhookFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDeployHandler(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	token := []byte(strings.Repeat("c", 64))
	digest := "sha256:" + strings.Repeat("b", 64)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := signRequest(secret, timestamp, digest)
	cases := []struct {
		name, method, path, body, timestamp, signature string
		submitFails                                    bool
		status                                         int
	}{
		{"accept", "POST", "/api/deploy/panel", digest, timestamp, signature, false, http.StatusAccepted},
		{"old path", "POST", "/api/depoly", digest, timestamp, signature, false, http.StatusNotFound},
		{"disabled upgrade path", "POST", "/api/deploy/deploy-tool", digest, timestamp, signature, false, http.StatusNotFound},
		{"trailing slash", "POST", "/api/deploy/panel/", digest, timestamp, signature, false, http.StatusNotFound},
		{"wrong method", "GET", deployPath, digest, timestamp, signature, false, http.StatusNotFound},
		{"oversized body", "POST", deployPath, digest + "\n", timestamp, signature, false, http.StatusBadRequest},
		{"invalid digest", "POST", deployPath, strings.ToUpper(digest), timestamp, signature, false, http.StatusBadRequest},
		{"missing signature", "POST", deployPath, digest, timestamp, "", false, http.StatusUnauthorized},
		{"altered digest", "POST", deployPath, digest[:70] + "c", timestamp, signature, false, http.StatusUnauthorized},
		{"failed submission", "POST", deployPath, digest, timestamp, signature, true, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queue := make(chan deployjob.Request, 1)
			handler := testHandler(t, secret, token, queue)
			if tc.submitFails {
				handler.submit = func(deployjob.Request) error { return errors.New("fixture submission failure") }
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.RemoteAddr = "172.18.0.2:12345"
			req.Header.Set("X-Real-IP", "192.0.2.10")
			req.Header.Set("Authorization", "Bearer "+string(token))
			req.Header.Set("X-Yagp-Timestamp", tc.timestamp)
			req.Header.Set("X-Yagp-Signature", tc.signature)
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tc.status {
				t.Fatalf("status = %d, want %d", resp.Code, tc.status)
			}
			select {
			case got := <-queue:
				if tc.status != http.StatusAccepted || got.Mode != "compose" || got.Digest != digest || !deployjob.ValidID(got.ID) {
					t.Fatalf("unexpected submitted job %+v", got)
				}
			default:
				if tc.status == http.StatusAccepted {
					t.Fatal("accepted request did not submit a job")
				}
			}
		})
	}
}

func TestWebhookSourceAndLimits(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	token := []byte(strings.Repeat("c", 64))
	digest := "sha256:" + strings.Repeat("b", 64)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	queue := make(chan deployjob.Request, 1)
	handler := testHandler(t, secret, token, queue)
	request := func(peer, ip, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", deployPath, strings.NewReader(digest))
		req.RemoteAddr = peer
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Yagp-Timestamp", timestamp)
		req.Header.Set("X-Yagp-Signature", signRequest(secret, timestamp, digest))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	const proxy, client = "172.18.0.2:12345", "192.0.2.10"
	if resp := request("192.0.2.99:12345", client, string(token)); resp.Code != http.StatusForbidden {
		t.Fatalf("untrusted proxy status = %d", resp.Code)
	}
	for _, invalidIP := range []string{"", client + ":80", client + ", 192.0.2.99", "fe80::1%eth0"} {
		if resp := request(proxy, invalidIP, string(token)); resp.Code != http.StatusForbidden {
			t.Fatalf("invalid forwarded IP %q status = %d", invalidIP, resp.Code)
		}
	}
	for range 4 {
		if resp := request(proxy, client, string(secret)); resp.Code != http.StatusUnauthorized {
			t.Fatalf("signing secret used as bearer token status = %d", resp.Code)
		}
	}
	if resp := request(proxy, client, string(secret)); resp.Code != http.StatusTooManyRequests || resp.Header().Get("Retry-After") != "900" {
		t.Fatalf("fifth auth failure status = %d, Retry-After = %q", resp.Code, resp.Header().Get("Retry-After"))
	}
	if resp := request(proxy, client, string(token)); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("banned client status = %d", resp.Code)
	}
	if resp := request(proxy, "2001:db8::2", string(token)); resp.Code != http.StatusAccepted {
		t.Fatalf("separate IPv6 client status = %d", resp.Code)
	}
	if resp := request(proxy, "192.0.2.11", string(token)); resp.Code != http.StatusConflict {
		t.Fatalf("replayed request status = %d", resp.Code)
	}
	timestamp = strconv.FormatInt(time.Now().Unix()+1, 10)
	resp := request(proxy, "192.0.2.11", string(token))
	retry, err := strconv.Atoi(resp.Header().Get("Retry-After"))
	if resp.Code != http.StatusTooManyRequests || err != nil || retry < 1 || retry > 300 {
		t.Fatalf("global cooldown status = %d, Retry-After = %q", resp.Code, resp.Header().Get("Retry-After"))
	}
}

func TestUpgradeHook(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	token := []byte(strings.Repeat("c", 64))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	queue := make(chan deployjob.Request, 1)
	handler := testHandler(t, []byte(strings.Repeat("d", 64)), []byte(strings.Repeat("e", 64)), queue)
	handler.upgrade = testHook(t, upgradeProtocol, secret, token)
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/deploy/panel", "upgrade", http.StatusUnauthorized},
		{"/api/depoly/upgrade", "upgrade", http.StatusNotFound},
		{"/api/deploy/deploy-tool/", "upgrade", http.StatusNotFound},
		{upgradePath, "0.1.2-abcdef0", http.StatusBadRequest},
		{upgradePath, "upgrade\n", http.StatusBadRequest},
		{"/api/deploy/deploy-tool", "upgrade", http.StatusAccepted},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.RemoteAddr = "172.18.0.2:12345"
		req.Header.Set("X-Real-IP", "192.0.2.10")
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("X-Yagp-Timestamp", timestamp)
		req.Header.Set("X-Yagp-Signature", signRequest(secret, timestamp, tc.body))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != tc.status {
			t.Fatalf("upgrade status for %q, %q = %d, want %d", tc.path, tc.body, resp.Code, tc.status)
		}
	}
	if len(queue) != 1 {
		t.Fatal("upgrade request did not submit a job")
	}
	if got := <-queue; got.Mode != "depoly" || got.Digest != "" || !deployjob.ValidID(got.ID) {
		t.Fatalf("unexpected submitted upgrade job %+v", got)
	}
}

func TestWebhookRoutesShareFailuresAndKeepIndependentState(t *testing.T) {
	panelSecret, panelToken := []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("b", 64))
	toolSecret, toolToken := []byte(strings.Repeat("c", 64)), []byte(strings.Repeat("d", 64))
	digest := "sha256:" + strings.Repeat("e", 64)
	queue := make(chan deployjob.Request, 2)
	handler := testHandler(t, panelSecret, panelToken, queue)
	handler.upgrade = testHook(t, upgradeProtocol, toolSecret, toolToken)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request := func(method, path, ip, body string, secret, token []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "172.18.0.2:12345"
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("X-Yagp-Timestamp", timestamp)
		req.Header.Set("X-Yagp-Signature", signRequest(secret, timestamp, body))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	const badIP, goodIP = "192.0.2.20", "192.0.2.21"
	for _, miss := range [][2]string{{"POST", "/api/deploy/unknown"}, {"GET", deployPath}, {"HEAD", upgradePath}} {
		if resp := request(miss[0], miss[1], badIP, "", nil, nil); resp.Code != http.StatusNotFound {
			t.Fatalf("route miss %v returned %d", miss, resp.Code)
		}
	}
	if resp := request("POST", deployPath, badIP, digest, toolSecret, toolToken); resp.Code != http.StatusUnauthorized {
		t.Fatalf("tool credentials on panel route returned %d", resp.Code)
	}
	if resp := request("POST", upgradePath, badIP, "upgrade", panelSecret, panelToken); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("cross-endpoint fifth failure returned %d", resp.Code)
	}
	if resp := request("POST", deployPath, badIP, digest, panelSecret, panelToken); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("panel request after shared ban returned %d", resp.Code)
	}
	if resp := request("POST", upgradePath, badIP, "upgrade", toolSecret, toolToken); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("tool request after shared ban returned %d", resp.Code)
	}
	if resp := request("GET", upgradePath, badIP, "", nil, nil); resp.Code != http.StatusNotFound {
		t.Fatalf("banned route miss returned %d", resp.Code)
	}
	const busyIP = "192.0.2.22"
	for i := range requestLimit {
		path, secret, token := deployPath, panelSecret, panelToken
		if i%2 == 0 {
			path, secret, token = upgradePath, toolSecret, toolToken
		}
		if resp := request("POST", path, busyIP, "invalid body", secret, token); resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid body on %s returned %d", path, resp.Code)
		}
	}
	if resp := request("POST", upgradePath, busyIP, "upgrade", toolSecret, toolToken); resp.Code != http.StatusTooManyRequests {
		t.Fatalf("requests to both routes did not share the per-IP quota: status=%d", resp.Code)
	}
	if resp := request("POST", deployPath, goodIP, digest, panelSecret, panelToken); resp.Code != http.StatusAccepted {
		t.Fatalf("panel admission returned %d", resp.Code)
	}
	if resp := request("POST", upgradePath, goodIP, "upgrade", toolSecret, toolToken); resp.Code != http.StatusAccepted {
		t.Fatalf("panel admission affected tool replay/cooldown, status=%d", resp.Code)
	}
	if len(queue) != 2 {
		t.Fatal("authenticated requests did not submit both jobs")
	}
	if panel, tool := <-queue, <-queue; panel.Mode != "compose" || panel.Digest != digest || tool.Mode != "depoly" || tool.Digest != "" || panel.ID == tool.ID {
		t.Fatalf("unexpected submitted jobs: panel=%+v tool=%+v", panel, tool)
	}
}

func TestWebhookMissesCountEvenAfterRequestLimit(t *testing.T) {
	token := []byte(strings.Repeat("b", 64))
	handler := testHandler(t, []byte(strings.Repeat("a", 64)), token, make(chan deployjob.Request, 1))
	request := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "172.18.0.2:12345"
		req.Header.Set("X-Real-IP", "192.0.2.20")
		req.Header.Set("Authorization", "Bearer "+string(token))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	for range requestLimit {
		if resp := request("POST", deployPath); resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid body with valid bearer token returned %d", resp.Code)
		}
	}
	for range authFailureLimit {
		if resp := request("POST", upgradePath); resp.Code != http.StatusNotFound {
			t.Fatalf("disabled upgrade while limited returned %d", resp.Code)
		}
	}
	resp := request("POST", deployPath)
	retry, err := strconv.Atoi(resp.Header().Get("Retry-After"))
	if resp.Code != http.StatusTooManyRequests || err != nil || retry <= int(requestWindow.Seconds()) {
		t.Fatalf("limited misses did not trigger ban: status=%d, Retry-After=%q", resp.Code, resp.Header().Get("Retry-After"))
	}
}

func TestPersistentAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accepted")
	now := time.Unix(1800000000, 123456789)
	limits, err := loadHookState(path)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := limits.accept(now.Unix(), now); retry != 0 || err != nil {
		t.Fatalf("first admission = %v, %v", retry, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("acceptance state permissions = %o", info.Mode().Perm())
	}
	restarted, err := loadHookState(path)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.accept(now.Unix(), now); retry != 0 || err != errReplay {
		t.Fatalf("replayed request after restart = %v, %v", retry, err)
	}
	if retry, err := restarted.accept(now.Unix()+1, now); retry != deployCooldown || err != nil {
		t.Fatalf("new request during persisted cooldown = %v, %v", retry, err)
	}
	if retry, err := restarted.accept(now.Unix()+1, now.Add(deployCooldown)); retry != 0 || err != nil {
		t.Fatalf("new request after cooldown = %v, %v", retry, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHookState(path); err == nil {
		t.Fatal("public acceptance state accepted")
	}
}

func TestWebhookPersistenceFailureDoesNotConsumeRequest(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	token := []byte(strings.Repeat("c", 64))
	digest := "sha256:" + strings.Repeat("b", 64)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	queue := make(chan deployjob.Request, 1)
	handler := testHandler(t, secret, token, queue)
	path := handler.panel.state.stateFile
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	request := func() int {
		req := httptest.NewRequest(http.MethodPost, deployPath, strings.NewReader(digest))
		req.RemoteAddr = "172.18.0.2:12345"
		req.Header.Set("X-Real-IP", "192.0.2.10")
		req.Header.Set("Authorization", "Bearer "+string(token))
		req.Header.Set("X-Yagp-Timestamp", timestamp)
		req.Header.Set("X-Yagp-Signature", signRequest(secret, timestamp, digest))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp.Code
	}
	if got := request(); got != http.StatusInternalServerError || len(queue) != 0 {
		t.Fatalf("failed persistence status = %d, queued = %d", got, len(queue))
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
		t.Fatalf("failed persistence left staging files: %v, %v", entries, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := request(); got != http.StatusAccepted || len(queue) != 1 {
		t.Fatalf("retry after persistence failure status = %d, queued = %d", got, len(queue))
	}
}

func TestInvalidAcceptanceStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acceptance")
	for _, invalid := range []string{"", "garbage", "0 100", "100 garbage"} {
		writeWebhookFixture(t, path, invalid)
		if _, err := loadHookState(path); err == nil {
			t.Fatalf("invalid acceptance state %q accepted", invalid)
		}
	}
	for _, parent := range []string{filepath.Join(dir, "missing"), path} {
		bad := filepath.Join(parent, "acceptance")
		if _, err := loadHookState(bad); err == nil {
			t.Fatalf("invalid acceptance path %s accepted", bad)
		}
		if err := persistAcceptance(bad, time.Now().Unix(), time.Now().Add(time.Minute)); err == nil {
			t.Fatalf("invalid acceptance path %s written", bad)
		}
	}
}

func TestHookCommands(t *testing.T) {
	for _, job := range []deployjob.Request{
		{ID: strings.Repeat("a", 32), Mode: "compose", Digest: "sha256:" + strings.Repeat("b", 64)},
		{ID: strings.Repeat("b", 32), Mode: "depoly"},
	} {
		cmd := updateCommand(t.Context(), job)
		if want := []string{"/usr/bin/sudo", "-n", "--", deployjob.Executable, job.Mode}; !reflect.DeepEqual(cmd.Args, want) {
			t.Fatalf("command = %v, want %v", cmd.Args, want)
		}
		var input deployjob.Request
		if err := json.NewDecoder(cmd.Stdin).Decode(&input); err != nil || input != job {
			t.Fatalf("command stdin = %+v, %v", input, err)
		}
	}
}

func TestRequestLimitsExpiry(t *testing.T) {
	now := time.Unix(1800000000, 0)
	ip := netip.MustParseAddr("192.0.2.10")
	limits := &requestLimits{clients: make(map[netip.Addr]*clientLimits)}
	for range requestLimit {
		if retry := limits.allowRequest(ip, now); retry != 0 {
			t.Fatalf("per-IP rate limit applied too soon: %v", retry)
		}
	}
	if retry := limits.allowRequest(ip, now); retry != time.Minute {
		t.Fatalf("per-IP rate limit retry = %v", retry)
	}
	now = now.Add(time.Minute)
	if retry := limits.allowRequest(ip, now); retry != 0 {
		t.Fatalf("expired request window still applied: %v", retry)
	}
	for range authFailureLimit {
		limits.authFailed(ip, now)
	}
	if retry := limits.allowRequest(ip, now.Add(time.Minute)); retry != 14*time.Minute {
		t.Fatalf("ban was cleared before its expiry: %v", retry)
	}
	if retry := limits.allowRequest(ip, now.Add(15*time.Minute)); retry != 0 {
		t.Fatalf("expired ban still applied: %v", retry)
	}
}

func TestRequestLimitsBound(t *testing.T) {
	now := time.Unix(1800000000, 0)
	limits := &requestLimits{clients: make(map[netip.Addr]*clientLimits)}
	for i := range maxTrackedIPs {
		ip := netip.AddrFrom4([4]byte{198, 18, byte(i >> 8), byte(i)})
		if retry := limits.allowRequest(ip, now); retry != 0 {
			t.Fatalf("address %s denied before capacity", ip)
		}
	}
	nextIP := netip.MustParseAddr("192.0.2.10")
	if retry := limits.allowRequest(nextIP, now); retry != time.Minute || len(limits.clients) != maxTrackedIPs {
		t.Fatalf("capacity limit did not bound address state: retry=%v, entries=%d", retry, len(limits.clients))
	}
	if retry := limits.allowRequest(nextIP, now.Add(time.Minute)); retry != 0 || len(limits.clients) != 1 {
		t.Fatalf("expired address state not cleaned up: retry=%v, entries=%d", retry, len(limits.clients))
	}
}

func TestValidSignatureTimeWindow(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	digest := "sha256:" + strings.Repeat("b", 64)
	now := time.Unix(1800000000, 0)
	for _, age := range []time.Duration{-301 * time.Second, -300 * time.Second, 0, 300 * time.Second, 301 * time.Second} {
		timestamp := strconv.FormatInt(now.Add(age).Unix(), 10)
		signature := signRequest(secret, timestamp, digest)
		seconds, valid := validSignature(secret, timestamp, signature, []byte(digest), now)
		want := age >= -signatureWindow && age <= signatureWindow
		if valid != want {
			t.Errorf("signature at %v valid = %t, want %t", age, valid, want)
		}
		if valid && seconds != now.Add(age).Unix() {
			t.Errorf("signature at %v returned wrong timestamp %d", age, seconds)
		}
	}
}

func TestReadCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if _, err := readCredential(path); err == nil {
		t.Fatal("missing secret accepted")
	}
	valid := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(valid+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if secret, err := readCredential(path); err != nil || string(secret) != valid {
		t.Fatalf("read valid secret failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.ToUpper(valid)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(path); err == nil {
		t.Fatal("uppercase secret accepted")
	}
}

func testHandler(t *testing.T, secret, token []byte, jobs chan<- deployjob.Request) deployHandler {
	t.Helper()
	return deployHandler{
		panel: testHook(t, panelProtocol, secret, token), trustedProxy: netip.MustParseAddr("172.18.0.2"),
		limits: &requestLimits{clients: make(map[netip.Addr]*clientLimits)},
		submit: func(job deployjob.Request) error {
			select {
			case jobs <- job:
				return nil
			default:
				t.Fatalf("unexpected extra update job %+v", job)
				return nil
			}
		},
	}
}

func testHook(t *testing.T, protocol hookProtocol, secret, token []byte) *deployHook {
	t.Helper()
	state, err := loadHookState(filepath.Join(t.TempDir(), "accepted"))
	if err != nil {
		t.Fatal(err)
	}
	return &deployHook{protocol: protocol, secret: secret, token: token, state: state}
}

func signRequest(secret []byte, timestamp, digest string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "\n" + digest))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
