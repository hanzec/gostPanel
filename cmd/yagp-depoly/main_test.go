package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
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
)

func TestServeRejectsInvalidConfiguration(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	token := filepath.Join(dir, "token")
	state := filepath.Join(dir, "state")
	writeWebhookFixture(t, secret, strings.Repeat("a", 64))
	writeWebhookFixture(t, token, strings.Repeat("b", 64))
	writeWebhookFixture(t, state, "bad state")
	executable := filepath.Join(dir, "yagp-depoly")
	for _, tc := range []struct {
		name, mode, executable string
		args                   []string
		want                   string
	}{
		{"unknown mode", "other", executable, nil, "unknown webhook mode"},
		{"unknown flag", "panel", executable, []string{"-bogus"}, "flag provided but not defined"},
		{"missing flags", "panel", executable, nil, "set -listen"},
		{"relative executable", "panel", "yagp-depoly", webhookArgs("127.0.0.1:0", secret, token, state), "set -listen"},
		{"missing secret", "panel", executable, webhookArgs("127.0.0.1:0", secret+".missing", token, state), "webhook signing secret"},
		{"missing token", "upgrade", executable, webhookArgs("127.0.0.1:0", secret, token+".missing", state), "webhook token"},
		{"identical credentials", "panel", executable, webhookArgs("127.0.0.1:0", secret, secret, state), "must differ"},
		{"corrupt state", "upgrade", executable, webhookArgs("127.0.0.1:0", secret, token, state), "load webhook acceptance state"},
		{"invalid listen", "panel", executable, webhookArgs("127.0.0.1:-1", secret, token, state+".new"), "listen for deployment webhooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := serve(tc.mode, tc.executable, tc.args)
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
	writeWebhookFixture(t, secret, strings.Repeat("a", 64))
	writeWebhookFixture(t, token, strings.Repeat("b", 64))
	notify, socket := listenNotifySocket(t, false)
	t.Setenv("NOTIFY_SOCKET", socket)
	err := serveWebhook("panel", filepath.Join(dir, "yagp-depoly"),
		webhookArgs("127.0.0.1:0", secret, token, filepath.Join(dir, "state")),
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
			request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+deployPath, strings.NewReader("sha256:"+strings.Repeat("a", 64)))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-Real-IP", "192.0.2.10")
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
			defer client.CloseIdleConnections()
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("real HTTP request without credentials = %d", response.StatusCode)
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
		full                                           bool
		status                                         int
	}{
		{"accept", "POST", "/api/depoly", digest, timestamp, signature, false, http.StatusAccepted},
		{"old path", "POST", "/_yagp/deploy", digest, timestamp, signature, false, http.StatusNotFound},
		{"upgrade path", "POST", "/api/depoly/upgrade", digest, timestamp, signature, false, http.StatusNotFound},
		{"trailing slash", "POST", "/api/depoly/", digest, timestamp, signature, false, http.StatusNotFound},
		{"wrong method", "GET", deployPath, digest, timestamp, signature, false, http.StatusMethodNotAllowed},
		{"oversized body", "POST", deployPath, digest + "\n", timestamp, signature, false, http.StatusBadRequest},
		{"invalid digest", "POST", deployPath, strings.ToUpper(digest), timestamp, signature, false, http.StatusBadRequest},
		{"missing signature", "POST", deployPath, digest, timestamp, "", false, http.StatusUnauthorized},
		{"altered digest", "POST", deployPath, digest[:70] + "c", timestamp, signature, false, http.StatusUnauthorized},
		{"full queue", "POST", deployPath, digest, timestamp, signature, true, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queue := make(chan string, 1)
			if tc.full {
				queue <- "pending"
			}
			handler := testHandler(t, secret, token, queue)
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
				if tc.full && got == "pending" {
					return
				}
				if tc.status != http.StatusAccepted || got != digest {
					t.Fatalf("unexpected queued digest %q", got)
				}
			default:
				if tc.status == http.StatusAccepted {
					t.Fatal("accepted request did not enqueue a digest")
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
	queue := make(chan string, 1)
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
	queue := make(chan string, 1)
	handler := testHandler(t, secret, token, queue)
	handler.protocol = upgradeProtocol
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/depoly", "upgrade", http.StatusNotFound},
		{"/_yagp/upgrade", "upgrade", http.StatusNotFound},
		{"/api/depoly/upgrade/", "upgrade", http.StatusNotFound},
		{upgradePath, "0.1.2-abcdef0", http.StatusBadRequest},
		{upgradePath, "upgrade\n", http.StatusBadRequest},
		{"/api/depoly/upgrade", "upgrade", http.StatusAccepted},
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
	if len(queue) != 1 || <-queue != "upgrade" {
		t.Fatal("upgrade request did not enqueue the expected trigger")
	}
}

func TestPersistentAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accepted")
	now := time.Unix(1800000000, 123456789)
	limits, err := loadRequestLimits(path)
	if err != nil {
		t.Fatal(err)
	}
	queue := make(chan string, 1)
	queue <- "pending"
	if retry, err := limits.enqueue(queue, "deploy", now.Unix(), now); retry != 0 || err != errQueueFull {
		t.Fatalf("full queue admission = %v, %v", retry, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("full queue wrote acceptance state: %v", err)
	}
	<-queue
	if retry, err := limits.enqueue(queue, "deploy", now.Unix(), now); retry != 0 || err != nil {
		t.Fatalf("first admission = %v, %v", retry, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("acceptance state permissions = %o", info.Mode().Perm())
	}
	<-queue
	restarted, err := loadRequestLimits(path)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.enqueue(queue, "replay", now.Unix(), now); retry != 0 || err != errReplay {
		t.Fatalf("replayed request after restart = %v, %v", retry, err)
	}
	if retry, err := restarted.enqueue(queue, "new", now.Unix()+1, now); retry != deployCooldown || err != nil {
		t.Fatalf("new request during persisted cooldown = %v, %v", retry, err)
	}
	if retry, err := restarted.enqueue(queue, "new", now.Unix()+1, now.Add(deployCooldown)); retry != 0 || err != nil {
		t.Fatalf("new request after cooldown = %v, %v", retry, err)
	}
	<-queue
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRequestLimits(path); err == nil {
		t.Fatal("public acceptance state accepted")
	}
}

func TestWebhookPersistenceFailureDoesNotConsumeRequest(t *testing.T) {
	secret := []byte(strings.Repeat("a", 64))
	token := []byte(strings.Repeat("c", 64))
	digest := "sha256:" + strings.Repeat("b", 64)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	queue := make(chan string, 1)
	handler := testHandler(t, secret, token, queue)
	path := handler.limits.stateFile
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
		if _, err := loadRequestLimits(path); err == nil {
			t.Fatalf("invalid acceptance state %q accepted", invalid)
		}
	}
	for _, parent := range []string{filepath.Join(dir, "missing"), path} {
		bad := filepath.Join(parent, "acceptance")
		if _, err := loadRequestLimits(bad); err == nil {
			t.Fatalf("invalid acceptance path %s accepted", bad)
		}
		if err := persistAcceptance(bad, time.Now().Unix(), time.Now().Add(time.Minute)); err == nil {
			t.Fatalf("invalid acceptance path %s written", bad)
		}
	}
}

func TestHookCommands(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	panel := hookCommand("panel", "/opt/yagp-panel/yagp-depoly", digest)
	if want := []string{"/usr/bin/sudo", "-n", "--", "/opt/yagp-panel/yagp-depoly", "deploy"}; !reflect.DeepEqual(panel.Args, want) {
		t.Fatalf("panel command = %v, want %v", panel.Args, want)
	}
	if input, err := io.ReadAll(panel.Stdin); err != nil || string(input) != digest+"\n" {
		t.Fatalf("panel command stdin = %q, %v", input, err)
	}
	upgrade := hookCommand("upgrade", "/opt/yagp-panel/yagp-depoly", "upgrade")
	if want := []string{"/usr/bin/sudo", "-n", "--", "/usr/bin/systemctl", "--no-block", "start", "yagp-depoly-upgrade.service"}; !reflect.DeepEqual(upgrade.Args, want) || upgrade.Stdin != nil {
		t.Fatalf("upgrade command = %v, want %v", upgrade.Args, want)
	}
}

func TestDeploymentWorkerReportsResult(t *testing.T) {
	for _, tc := range []struct {
		mode, value string
		result      error
		want        string
	}{
		{"panel", "sha256:" + strings.Repeat("a", 64), nil, "deployment completed"},
		{"upgrade", "upgrade", nil, "requested yagp-depoly-upgrade.service"},
		{"panel", "sha256:" + strings.Repeat("b", 64), errors.New("deployment failed in fixture"), "deployment failed in fixture"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			t.Cleanup(func() { log.SetOutput(previous) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			queue := make(chan string, 1)
			queue <- tc.value
			runDeployments(ctx, queue, tc.mode, func(value string) error {
				cancel()
				if value != tc.value {
					t.Fatalf("worker input = %q, want %q", value, tc.value)
				}
				return tc.result
			})
			if got := output.String(); !strings.Contains(got, "starting "+tc.mode+" webhook task") || !strings.Contains(got, tc.want) {
				t.Fatalf("worker log = %q, want start and %q", got, tc.want)
			}
		})
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

func testHandler(t *testing.T, secret, token []byte, queue chan<- string) deployHandler {
	t.Helper()
	limits, err := loadRequestLimits(filepath.Join(t.TempDir(), "accepted"))
	if err != nil {
		t.Fatal(err)
	}
	return deployHandler{
		protocol: panelProtocol, secret: secret, token: token, queue: queue,
		trustedProxy: netip.MustParseAddr("172.18.0.2"), limits: limits,
	}
}

func signRequest(secret []byte, timestamp, digest string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp + "\n" + digest))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
