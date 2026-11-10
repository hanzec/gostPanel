package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yagp/internal/deployjob"
)

const (
	deployPath       = "/api/deploy/panel"
	upgradePath      = "/api/deploy/deploy-tool"
	digestPrefix     = "sha256:"
	signaturePrefix  = "sha256="
	digestLength     = int64(len(digestPrefix) + 2*sha256.Size)
	signatureWindow  = 5 * time.Minute
	requestWindow    = time.Minute
	requestLimit     = 20
	authFailureLimit = 5
	banDuration      = 15 * time.Minute
	deployCooldown   = 5 * time.Minute
	maxTrackedIPs    = 4096
)

var (
	lowerHex  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	errReplay = errors.New("webhook timestamp was already accepted or is older")

	panelProtocol   = hookProtocol{"compose", digestLength, validDigest}
	upgradeProtocol = hookProtocol{
		"depoly", int64(len("upgrade")), func(value string) bool { return value == "upgrade" },
	}
)

type hookProtocol struct {
	mode      string
	bodyLimit int64
	validate  func(string) bool
}

func serve(args []string) error {
	info, err := os.Stat(deployjob.Executable)
	if err != nil {
		return fmt.Errorf("check installed yagp-update: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("installed yagp-update must be an executable file")
	}
	return serveWebhook(args, (*http.Server).Serve)
}

func serveWebhook(args []string, runServer func(*http.Server, net.Listener) error) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "", "HTTP listen address")
	secretFile := flags.String("secret-file", "", "path to the panel webhook signing secret file")
	tokenFile := flags.String("token-file", "", "path to the panel webhook bearer token file")
	proxy := flags.String("trusted-proxy", "", "IPv4 address of the Traefik container")
	stateFile := flags.String("state-file", "", "absolute path to the panel webhook acceptance state file")
	upgradeSecret := flags.String("upgrade-secret-file", "", "path to the tool upgrade webhook signing secret file")
	upgradeToken := flags.String("upgrade-token-file", "", "path to the tool upgrade webhook bearer token file")
	upgradeState := flags.String("upgrade-state-file", "", "absolute path to the tool upgrade webhook acceptance state file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	trustedProxy, err := netip.ParseAddr(*proxy)
	if *listen == "" || *secretFile == "" || *tokenFile == "" ||
		!filepath.IsAbs(*stateFile) || err != nil || !trustedProxy.Is4() ||
		trustedProxy.IsUnspecified() || flags.NArg() != 0 {
		return fmt.Errorf("set -listen, -secret-file, -token-file, -trusted-proxy (IPv4), and -state-file (absolute path)")
	}
	panel, err := loadDeployHook(panelProtocol, *secretFile, *tokenFile, *stateFile)
	if err != nil {
		return fmt.Errorf("panel webhook: %w", err)
	}
	var upgrade *deployHook
	if *upgradeSecret != "" || *upgradeToken != "" || *upgradeState != "" {
		if *upgradeSecret == "" || *upgradeToken == "" || !filepath.IsAbs(*upgradeState) {
			return fmt.Errorf("set all of -upgrade-secret-file, -upgrade-token-file, and -upgrade-state-file (absolute path)")
		}
		if filepath.Clean(*stateFile) == filepath.Clean(*upgradeState) {
			return fmt.Errorf("panel and tool upgrade webhook state files must differ")
		}
		upgrade, err = loadDeployHook(upgradeProtocol, *upgradeSecret, *upgradeToken, *upgradeState)
		if err != nil {
			return fmt.Errorf("tool upgrade webhook: %w", err)
		}
		if hmac.Equal(panel.secret, upgrade.secret) || hmac.Equal(panel.secret, upgrade.token) ||
			hmac.Equal(panel.token, upgrade.secret) || hmac.Equal(panel.token, upgrade.token) {
			return fmt.Errorf("panel and tool upgrade webhook credentials must differ")
		}
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen for deployment webhooks: %w", err)
	}
	defer listener.Close()
	if err := notifyReady(os.Getenv("NOTIFY_SOCKET")); err != nil {
		return fmt.Errorf("notify systemd: %w", err)
	}

	server := &http.Server{
		Handler: deployHandler{
			panel: panel, upgrade: upgrade, trustedProxy: trustedProxy,
			limits:    &requestLimits{clients: make(map[netip.Addr]*clientLimits)},
			submit:    submitUpdate,
			directory: deployjob.Directory,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    4096,
	}
	log.Printf("deployment webhook listening on %s", listener.Addr())
	return runServer(server, listener)
}

func loadDeployHook(protocol hookProtocol, secretFile, tokenFile, stateFile string) (*deployHook, error) {
	secret, err := readCredential(secretFile)
	if err != nil {
		return nil, fmt.Errorf("webhook signing secret: %w", err)
	}
	token, err := readCredential(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("webhook token: %w", err)
	}
	if hmac.Equal(secret, token) {
		return nil, fmt.Errorf("webhook token and signing secret must differ")
	}
	state, err := loadHookState(stateFile)
	if err != nil {
		return nil, fmt.Errorf("load webhook acceptance state: %w", err)
	}
	return &deployHook{
		protocol: protocol, secret: secret, token: token, state: state,
	}, nil
}

func notifyReady(socket string) error {
	if socket == "" {
		return nil
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	syscall.CloseOnExec(fd)
	defer syscall.Close(fd)
	return syscall.Sendto(fd, []byte("READY=1"), syscall.MSG_DONTWAIT, &syscall.SockaddrUnix{Name: socket})
}

func readCredential(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credential: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if !lowerHex.MatchString(secret) {
		return nil, fmt.Errorf("credential must be 64 lowercase hex characters")
	}
	return []byte(secret), nil
}

type deployHook struct {
	protocol      hookProtocol
	secret, token []byte
	state         *hookState
}

type deployHandler struct {
	panel, upgrade *deployHook
	trustedProxy   netip.Addr
	limits         *requestLimits
	submit         func(deployjob.Request) error
	directory      string
}

func (h deployHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var hook *deployHook
	path := r.URL.EscapedPath()
	statusRequest := r.Method == http.MethodGet && strings.HasPrefix(path, statusPath)
	if r.Method == http.MethodPost {
		switch path {
		case deployPath:
			hook = h.panel
		case upgradePath:
			hook = h.upgrade
		}
	}
	ip, ok := h.clientIP(r)
	now := time.Now()
	if hook == nil && !statusRequest {
		if ok {
			h.limits.allowRequest(ip, now)
			h.limits.authFailed(ip, now)
		}
		http.NotFound(w, r)
		return
	}
	if !ok {
		http.Error(w, "invalid webhook source", http.StatusForbidden)
		return
	}
	if retry := h.limits.allowRequest(ip, now); retry > 0 {
		rateLimited(w, retry)
		return
	}
	if statusRequest {
		h.serveStatus(w, r, ip, now)
		return
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !hmac.Equal([]byte(bearer), hook.token) {
		h.rejectAuth(w, ip, now)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hook.protocol.bodyLimit))
	if err != nil {
		http.Error(w, "invalid webhook body", http.StatusBadRequest)
		return
	}
	value := string(body)
	if !hook.protocol.validate(value) {
		http.Error(w, "invalid webhook body", http.StatusBadRequest)
		return
	}
	now = time.Now()
	timestamp, ok := validSignature(hook.secret, r.Header.Get("X-Yagp-Timestamp"), r.Header.Get("X-Yagp-Signature"), body, now)
	if !ok {
		h.rejectAuth(w, ip, now)
		return
	}
	var randomID [16]byte
	rand.Read(randomID[:])
	job := deployjob.Request{ID: hex.EncodeToString(randomID[:]), Mode: hook.protocol.mode}
	if job.Mode == "compose" {
		job.Digest = value
	}
	// Record acceptance first, since a submission timeout can occur after systemd has started the job.
	retry, err := hook.state.accept(timestamp, now)
	switch {
	case retry > 0:
		rateLimited(w, retry)
	case err == errReplay:
		http.Error(w, errReplay.Error(), http.StatusConflict)
	case err != nil:
		log.Printf("could not persist webhook acceptance state: %v", err)
		http.Error(w, "could not record webhook acceptance", http.StatusInternalServerError)
	default:
		h.submitJob(w, hook, job)
	}
}

func (h deployHandler) submitJob(w http.ResponseWriter, hook *deployHook, job deployjob.Request) {
	if err := h.submit(job); err != nil {
		log.Printf("could not submit %s job %s: %v", job.Mode, job.ID, err)
		http.Error(w, "could not submit update", http.StatusServiceUnavailable)
		return
	}
	log.Printf("submitted %s job %s", job.Mode, job.ID)
	link := signedStatusURL(hook.secret, job, time.Now().Add(statusLinkLifetime))
	w.Header().Set("Location", link)
	writeStatusJSON(w, http.StatusAccepted, struct {
		StatusURL string `json:"status_url"`
	}{link})
}

func validDigest(value string) bool {
	hash, ok := strings.CutPrefix(value, digestPrefix)
	return ok && lowerHex.MatchString(hash)
}

func (h deployHandler) clientIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || peer.Unmap() != h.trustedProxy {
		return netip.Addr{}, false
	}
	forwarded := r.Header.Values("X-Real-IP")
	if len(forwarded) != 1 {
		return netip.Addr{}, false
	}
	client, err := netip.ParseAddr(forwarded[0])
	return client.Unmap(), err == nil && client.Zone() == ""
}

func (h deployHandler) rejectAuth(w http.ResponseWriter, ip netip.Addr, now time.Time) {
	if retry := h.limits.authFailed(ip, now); retry > 0 {
		rateLimited(w, retry)
		return
	}
	http.Error(w, "invalid webhook credentials", http.StatusUnauthorized)
}

func rateLimited(w http.ResponseWriter, retry time.Duration) {
	seconds := (retry + time.Second - 1) / time.Second
	w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	http.Error(w, "deployment request rate limited", http.StatusTooManyRequests)
}

type clientLimits struct {
	windowEnds  time.Time
	bannedUntil time.Time
	requests    int
	failures    int
}

type requestLimits struct {
	mu        sync.Mutex
	clients   map[netip.Addr]*clientLimits
	nextSweep time.Time
}

type hookState struct {
	mu           sync.Mutex
	stateFile    string
	lastAccepted int64
	nextDeploy   time.Time
}

func loadHookState(path string) (*hookState, error) {
	state := &hookState{stateFile: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if dir, err := os.Stat(filepath.Dir(path)); err != nil {
			return nil, err
		} else if !dir.IsDir() {
			return nil, fmt.Errorf("acceptance state parent is not a directory")
		}
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("acceptance state must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return nil, fmt.Errorf("invalid acceptance state")
	}
	last, lastErr := strconv.ParseInt(fields[0], 10, 64)
	next, nextErr := strconv.ParseInt(fields[1], 10, 64)
	if lastErr != nil || nextErr != nil || last <= 0 || next <= 0 {
		return nil, fmt.Errorf("invalid acceptance state")
	}
	state.lastAccepted = last
	state.nextDeploy = time.Unix(0, next)
	return state, nil
}

func persistAcceptance(path string, timestamp int64, nextDeploy time.Time) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	file, err := os.CreateTemp(dir.Name(), ".acceptance-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%d %d\n", timestamp, nextDeploy.UnixNano()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return dir.Sync()
}

func (l *requestLimits) allowRequest(ip netip.Addr, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.nextSweep.After(now) {
		for addr, client := range l.clients {
			if !client.windowEnds.After(now) && !client.bannedUntil.After(now) {
				delete(l.clients, addr)
			}
		}
		l.nextSweep = now.Add(requestWindow)
	}
	client := l.clients[ip]
	if client == nil {
		if len(l.clients) >= maxTrackedIPs {
			return l.nextSweep.Sub(now)
		}
		client = &clientLimits{}
		l.clients[ip] = client
	}
	if client.bannedUntil.After(now) {
		return client.bannedUntil.Sub(now)
	}
	if !client.windowEnds.After(now) {
		*client = clientLimits{windowEnds: now.Add(requestWindow)}
	}
	if client.requests >= requestLimit {
		return client.windowEnds.Sub(now)
	}
	client.requests++
	return 0
}

func (l *requestLimits) authFailed(ip netip.Addr, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	client := l.clients[ip]
	if client == nil || !client.windowEnds.After(now) {
		return 0
	}
	if client.bannedUntil.After(now) {
		return client.bannedUntil.Sub(now)
	}
	client.failures++
	if client.failures < authFailureLimit {
		return 0
	}
	client.bannedUntil = now.Add(banDuration)
	log.Printf("blocked webhook client %s for %s after repeated route misses or authentication failures", ip, banDuration)
	return banDuration
}

func (s *hookState) accept(timestamp int64, now time.Time) (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if timestamp <= s.lastAccepted {
		return 0, errReplay
	}
	if s.nextDeploy.After(now) {
		return s.nextDeploy.Sub(now), nil
	}
	nextDeploy := now.Add(deployCooldown)
	if err := persistAcceptance(s.stateFile, timestamp, nextDeploy); err != nil {
		return 0, err
	}
	s.lastAccepted = timestamp
	s.nextDeploy = nextDeploy
	return 0, nil
}

func validSignature(secret []byte, timestamp, header string, body []byte, now time.Time) (int64, bool) {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	window := int64(signatureWindow / time.Second)
	if err != nil || seconds <= 0 || strconv.FormatInt(seconds, 10) != timestamp ||
		seconds < now.Unix()-window || seconds > now.Unix()+window {
		return 0, false
	}
	signature, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return 0, false
	}
	mac := hmac.New(sha256.New, secret)
	io.WriteString(mac, timestamp+"\n")
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return seconds, hmac.Equal([]byte(signature), []byte(expected))
}

func updateCommand(ctx context.Context, job deployjob.Request) *exec.Cmd {
	input, _ := json.Marshal(job)
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "--", deployjob.Executable, job.Mode)
	cmd.Stdin = strings.NewReader(string(input) + "\n")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func submitUpdate(job deployjob.Request) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return updateCommand(ctx, job).Run()
}
