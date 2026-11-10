package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
		{"accept", "POST", deployPath, digest, timestamp, signature, false, http.StatusAccepted},
		{"wrong path", "POST", "/deploy", digest, timestamp, signature, false, http.StatusNotFound},
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
		{deployPath, "upgrade", http.StatusNotFound},
		{upgradePath, "0.1.2-abcdef0", http.StatusBadRequest},
		{upgradePath, "upgrade\n", http.StatusBadRequest},
		{upgradePath, "upgrade", http.StatusAccepted},
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
