package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"time"
)

const (
	deployPath       = "/_yagp/deploy"
	upgradePath      = "/_yagp/upgrade"
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
	lowerHex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	errReplay    = errors.New("webhook timestamp was already accepted or is older")
	errQueueFull = errors.New("deployment queue is full")

	panelProtocol   = hookProtocol{deployPath, digestLength, validDigest}
	upgradeProtocol = hookProtocol{
		upgradePath, int64(len("upgrade")), func(value string) bool { return value == "upgrade" },
	}
)

type hookProtocol struct {
	path      string
	bodyLimit int64
	validate  func(string) bool
}

func serve(mode, executable string, args []string) error {
	return serveWebhook(mode, executable, args, (*http.Server).Serve)
}

func serveWebhook(mode, executable string, args []string, runServer func(*http.Server, net.Listener) error) error {
	var protocol hookProtocol
	switch mode {
	case "panel":
		protocol = panelProtocol
	case "upgrade":
		protocol = upgradeProtocol
	default:
		return fmt.Errorf("unknown webhook mode %q", mode)
	}
	flags := flag.NewFlagSet("serve "+mode, flag.ContinueOnError)
	listen := flags.String("listen", "", "HTTP listen address")
	secretFile := flags.String("secret-file", "", "path to the webhook secret file")
	tokenFile := flags.String("token-file", "", "path to the webhook bearer token file")
	proxy := flags.String("trusted-proxy", "", "IPv4 address of the Traefik container")
	stateFile := flags.String("state-file", "", "absolute path to the webhook acceptance state file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	trustedProxy, err := netip.ParseAddr(*proxy)
	if *listen == "" || !filepath.IsAbs(executable) || *secretFile == "" || *tokenFile == "" ||
		!filepath.IsAbs(*stateFile) || err != nil || !trustedProxy.Is4() ||
		trustedProxy.IsUnspecified() || flags.NArg() != 0 {
		return fmt.Errorf("set -listen, -secret-file, -token-file, -trusted-proxy (IPv4), and -state-file (absolute path)")
	}
	secret, err := readCredential(*secretFile)
	if err != nil {
		return fmt.Errorf("webhook signing secret: %w", err)
	}
	token, err := readCredential(*tokenFile)
	if err != nil {
		return fmt.Errorf("webhook token: %w", err)
	}
	if hmac.Equal(secret, token) {
		return fmt.Errorf("webhook token and signing secret must differ")
	}
	limits, err := loadRequestLimits(*stateFile)
	if err != nil {
		return fmt.Errorf("load webhook acceptance state: %w", err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen for deployment webhooks: %w", err)
	}
	defer listener.Close()
	if err := notifyReady(os.Getenv("NOTIFY_SOCKET")); err != nil {
		return fmt.Errorf("notify systemd: %w", err)
	}

	queue := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runDeployments(ctx, queue, mode, func(value string) error {
		return hookCommand(mode, executable, value).Run()
	})
	server := &http.Server{
		Handler: deployHandler{
			protocol: protocol, secret: secret, token: token, trustedProxy: trustedProxy,
			queue: queue, limits: limits,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    4096,
	}
	log.Printf("%s webhook listening on %s", mode, listener.Addr())
	return runServer(server, listener)
}

func notifyReady(socket string) error {
	if socket == "" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte("READY=1"))
	return err
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

type deployHandler struct {
	protocol      hookProtocol
	secret, token []byte
	trustedProxy  netip.Addr
	queue         chan<- string
	limits        *requestLimits
}

func (h deployHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != h.protocol.path {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip, ok := h.clientIP(r)
	if !ok {
		http.Error(w, "invalid webhook source", http.StatusForbidden)
		return
	}
	now := time.Now()
	if retry := h.limits.allowRequest(ip, now); retry > 0 {
		rateLimited(w, retry)
		return
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !hmac.Equal([]byte(bearer), h.token) {
		h.rejectAuth(w, ip, now)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.protocol.bodyLimit))
	if err != nil {
		http.Error(w, "invalid webhook body", http.StatusBadRequest)
		return
	}
	value := string(body)
	if !h.protocol.validate(value) {
		http.Error(w, "invalid webhook body", http.StatusBadRequest)
		return
	}
	now = time.Now()
	timestamp, ok := validSignature(h.secret, r.Header.Get("X-Yagp-Timestamp"), r.Header.Get("X-Yagp-Signature"), body, now)
	if !ok {
		h.rejectAuth(w, ip, now)
		return
	}
	switch retry, err := h.limits.enqueue(h.queue, value, timestamp, now); {
	case retry > 0:
		rateLimited(w, retry)
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case err == errReplay:
		http.Error(w, errReplay.Error(), http.StatusConflict)
	case err == errQueueFull:
		http.Error(w, errQueueFull.Error(), http.StatusServiceUnavailable)
	default:
		log.Printf("could not persist webhook acceptance state: %v", err)
		http.Error(w, "could not record webhook acceptance", http.StatusInternalServerError)
	}
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
	mu           sync.Mutex
	clients      map[netip.Addr]*clientLimits
	nextSweep    time.Time
	stateFile    string
	lastAccepted int64
	nextDeploy   time.Time
}

func loadRequestLimits(path string) (*requestLimits, error) {
	limits := &requestLimits{stateFile: path, clients: make(map[netip.Addr]*clientLimits)}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if dir, err := os.Stat(filepath.Dir(path)); err != nil {
			return nil, err
		} else if !dir.IsDir() {
			return nil, fmt.Errorf("acceptance state parent is not a directory")
		}
		return limits, nil
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
	limits.lastAccepted = last
	limits.nextDeploy = time.Unix(0, next)
	return limits, nil
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
	log.Printf("blocked webhook client %s for %s after repeated authentication failures", ip, banDuration)
	return banDuration
}

func (l *requestLimits) enqueue(queue chan<- string, value string, timestamp int64, now time.Time) (time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if timestamp <= l.lastAccepted {
		return 0, errReplay
	}
	if l.nextDeploy.After(now) {
		return l.nextDeploy.Sub(now), nil
	}
	if len(queue) >= cap(queue) {
		return 0, errQueueFull
	}
	nextDeploy := now.Add(deployCooldown)
	if err := persistAcceptance(l.stateFile, timestamp, nextDeploy); err != nil {
		return 0, err
	}
	// Handlers produce work under this lock. The worker can only free queue slots.
	l.lastAccepted = timestamp
	l.nextDeploy = nextDeploy
	queue <- value
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

func hookCommand(mode, executable, value string) *exec.Cmd {
	var cmd *exec.Cmd
	if mode == "upgrade" {
		cmd = exec.Command("/usr/bin/sudo", "-n", "--", "/usr/bin/systemctl", "--no-block", "start", "yagp-depoly-upgrade.service")
	} else {
		cmd = exec.Command("/usr/bin/sudo", "-n", "--", executable, "deploy")
		cmd.Stdin = strings.NewReader(value + "\n")
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func runDeployments(ctx context.Context, queue <-chan string, mode string, execute func(string) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case value := <-queue:
			log.Printf("starting %s webhook task %s", mode, value)
			if err := execute(value); err != nil {
				log.Printf("%s webhook task %s failed: %v", mode, value, err)
			} else if mode == "upgrade" {
				log.Print("requested yagp-depoly-upgrade.service")
			} else {
				log.Printf("deployment completed for %s", value)
			}
		}
	}
}
