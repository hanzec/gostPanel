package nodegrpc

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestParseRegistrationTokenID(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	fingerprint := strings.Repeat("a1", 32)
	for _, test := range []struct {
		token       string
		id          uint64
		fingerprint string
	}{
		{"1." + secret, 1, ""},
		{"4294967295." + secret, 4294967295, ""},
		{"4294967295." + secret + "." + fingerprint, 4294967295, fingerprint},
		{"4294967296." + secret, 0, ""},
		{"0." + secret, 0, ""},
		{"1." + secret[:42] + "B", 0, ""},
		{"1." + secret[:42] + "?", 0, ""},
		{"1." + secret + "=", 0, ""},
		{"1." + secret[:42], 0, ""},
		{"1", 0, ""},
		{"1." + secret + ".", 0, ""},
		{"1." + secret + "." + fingerprint[:63], 0, ""},
		{"1." + secret + "." + fingerprint + ".extra", 0, ""},
		{"1." + secret + "." + strings.ToUpper(fingerprint), 0, ""},
		{"1." + secret + "." + fingerprint[:63] + "g", 0, ""},
	} {
		t.Run(test.token, func(t *testing.T) {
			id, fingerprint, err := ParseRegistrationToken(test.token)
			if id != test.id || fingerprint != test.fingerprint || (err != nil) != (test.id == 0) {
				t.Fatalf("parsed ID and fingerprint = %d, %q, %v, want %d, %q", id, fingerprint, err, test.id, test.fingerprint)
			}
			if parsedID, err := ParseRegistrationTokenID(test.token); parsedID != id || (err != nil) != (test.id == 0) {
				t.Fatalf("parsed ID = %d, %v, want %d", parsedID, err, id)
			}
		})
	}
}

func TestObserverAuthentication(t *testing.T) {
	const username, password = "node-42", "password:with-colon"
	token := ObserverToken(username, password)
	if len(token) != 64 || ObserverToken(username, "wrong") == token {
		t.Fatal("observer token was not derived from both node credentials")
	}
	for _, test := range []struct {
		name   string
		tokens []string
		ok     bool
	}{
		{"correct", []string{token}, true},
		{"absent", nil, false},
		{"duplicates", []string{token, token}, false},
		{"wrong", []string{"wrong"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"token": test.tokens})
			if got := MatchObserverToken(ctx, token); got != test.ok {
				t.Errorf("observer token accepted = %t, want %t", got, test.ok)
			}
		})
	}
}

func TestClientTLSLoadsExplicitRoots(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-ca.pem")
	panel, _ := ParsePanelHost("127.0.0.1")
	if _, err := panel.TLSConfig(path, ""); err == nil {
		t.Fatal("missing explicit CA was accepted")
	}
	if err := os.WriteFile(path, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if creds, err := panel.TLSConfig(path, ""); err != nil || creds.RootCAs == nil {
		t.Fatalf("client with explicit CA = %v, %v", creds, err)
	}
	if creds, err := panel.TLSConfig("", ""); err != nil || creds.RootCAs != nil {
		t.Fatalf("client with system roots = %v, %v", creds, err)
	}
}

func TestPinnedTLSRejectsAnUnexpectedPanelIdentity(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	digest := sha256.Sum256(server.Certificate().Raw)
	for _, tt := range []struct {
		name, host, fingerprint string
		wantError               func(error) bool
	}{
		{"wrong hostname", "wrong.example", hex.EncodeToString(digest[:]), func(err error) bool {
			return errors.As(err, new(x509.HostnameError))
		}},
		{"wrong fingerprint", "127.0.0.1", strings.Repeat("0", 64), func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "fingerprint mismatch")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			panel, err := ParsePanelHost(tt.host)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := panel.TLSConfig("", "invalid-fingerprint"); err == nil {
				t.Fatal("malformed fingerprint was accepted")
			}
			transport, err := panel.TLSConfig("", tt.fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if err := tls.Client(raw, transport).HandshakeContext(t.Context()); !tt.wantError(err) {
				t.Fatalf("pinned TLS handshake = %v", err)
			}
		})
	}
}
