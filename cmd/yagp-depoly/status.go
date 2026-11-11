package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"yagp/internal/deployjob"
)

const (
	statusPath            = "/api/deploy/status/"
	statusLinkLifetime    = 20 * time.Minute
	statusRefreshInterval = time.Second
	statusPingInterval    = 20 * time.Second
	statusWriteTimeout    = 5 * time.Second
)

func signedStatusURL(secret []byte, job deployjob.Request, until time.Time) string {
	expires := strconv.FormatInt(until.Unix(), 10)
	query := url.Values{
		"expires":   {expires},
		"mode":      {job.Mode},
		"signature": {statusSignature(secret, job.ID, job.Mode, expires)},
	}
	return statusPath + job.ID + "?" + query.Encode()
}

func statusSignature(secret []byte, id, mode, expires string) string {
	mac := hmac.New(sha256.New, secret)
	io.WriteString(mac, "yagp-deploy-status-v1\n"+id+"\n"+mode+"\n"+expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h deployHandler) serveStatus(w http.ResponseWriter, r *http.Request, ip netip.Addr, now time.Time) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	id := strings.TrimPrefix(r.URL.EscapedPath(), statusPath)
	mode, expiry := h.statusLinkMode(id, r.URL.RawQuery, now)
	if mode == "" {
		h.rejectAuth(w, ip, now)
		return
	}
	status, err := deployjob.ReadStatus(h.directory, id)
	if errors.Is(err, os.ErrNotExist) {
		rejectUnsupportedRequest(w, r, "update job not found")
		return
	}
	if err != nil {
		log.Printf("could not read update job %s: %v", id, err)
		http.Error(w, "could not read update status", http.StatusInternalServerError)
		return
	}
	if status.Mode != mode {
		rejectUnsupportedRequest(w, r, "update job mode mismatch")
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		h.serveStatusStream(w, r, id, expiry, status)
		return
	}
	writeStatusJSON(w, http.StatusOK, status)
}

func (h deployHandler) serveStatusStream(w http.ResponseWriter, r *http.Request, id string, expiry time.Time, status deployjob.Status) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// The WebSocket owns the connection after the HTTP upgrade.
	ctx, cancel := context.WithDeadline(context.Background(), expiry)
	defer cancel()
	ctx = conn.CloseRead(ctx)

	write := func(status deployjob.Status) error {
		writeCtx, cancel := context.WithTimeout(ctx, statusWriteTimeout)
		defer cancel()
		return wsjson.Write(writeCtx, conn, status)
	}
	if err := write(status); err != nil {
		return
	}
	refresh := time.NewTicker(statusRefreshInterval)
	defer refresh.Stop()
	ping := time.NewTicker(statusPingInterval)
	defer ping.Stop()
	for status.State != "succeeded" && status.State != "failed" {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, statusWriteTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		case <-refresh.C:
			next, err := deployjob.ReadStatus(h.directory, id)
			if err != nil || next.Mode != status.Mode {
				log.Printf("could not stream update job %s: mode=%q err=%v", id, next.Mode, err)
				_ = conn.Close(websocket.StatusInternalError, "could not read update status")
				return
			}
			if next.State == status.State && next.Logs == status.Logs {
				continue
			}
			if err := write(next); err != nil {
				return
			}
			status = next
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func (h deployHandler) statusLinkMode(id, rawQuery string, now time.Time) (string, time.Time) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || !deployjob.ValidID(id) || len(query) != 3 ||
		len(query["expires"]) != 1 || len(query["mode"]) != 1 || len(query["signature"]) != 1 {
		return "", time.Time{}
	}
	expires := query.Get("expires")
	until, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || strconv.FormatInt(until, 10) != expires ||
		until <= now.Unix() || until > now.Add(statusLinkLifetime).Unix() {
		return "", time.Time{}
	}
	var hook *deployHook
	mode := query.Get("mode")
	switch mode {
	case "compose":
		hook = h.panel
	case "depoly":
		hook = h.upgrade
	}
	if hook == nil || !hmac.Equal([]byte(query.Get("signature")), []byte(statusSignature(h.secret, id, mode, expires))) {
		return "", time.Time{}
	}
	return mode, time.Unix(until, 0)
}

func writeStatusJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("could not write webhook response: %v", err)
	}
}
