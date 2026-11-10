package main

import (
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

	"yagp/internal/deployjob"
)

const (
	statusPath         = "/api/deploy/status/"
	statusLinkLifetime = 20 * time.Minute
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
	mode := h.statusLinkMode(id, r.URL.RawQuery, now)
	if mode == "" {
		h.rejectAuth(w, ip, now)
		return
	}
	status, err := deployjob.ReadStatus(h.directory, id)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("could not read update job %s: %v", id, err)
		http.Error(w, "could not read update status", http.StatusInternalServerError)
		return
	}
	if status.Mode != mode {
		http.NotFound(w, r)
		return
	}
	writeStatusJSON(w, http.StatusOK, status)
}

func (h deployHandler) statusLinkMode(id, rawQuery string, now time.Time) string {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || !deployjob.ValidID(id) || len(query) != 3 ||
		len(query["expires"]) != 1 || len(query["mode"]) != 1 || len(query["signature"]) != 1 {
		return ""
	}
	expires := query.Get("expires")
	until, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || strconv.FormatInt(until, 10) != expires ||
		until <= now.Unix() || until > now.Add(statusLinkLifetime).Unix() {
		return ""
	}
	var hook *deployHook
	mode := query.Get("mode")
	switch mode {
	case "compose":
		hook = h.panel
	case "depoly":
		hook = h.upgrade
	}
	if hook == nil || !hmac.Equal([]byte(query.Get("signature")), []byte(statusSignature(hook.secret, id, mode, expires))) {
		return ""
	}
	return mode
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
