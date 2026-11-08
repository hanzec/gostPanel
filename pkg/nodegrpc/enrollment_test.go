package nodegrpc

import (
	"context"
	"crypto/sha256"
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

func TestCredentialAndObserverAuthentication(t *testing.T) {
	const username, password = "node-42", "password:with-colon"
	credentials := PasswordCredentials(username, password)
	values, err := credentials.GetRequestMetadata(context.Background())
	if err != nil || !credentials.RequireTransportSecurity() {
		t.Fatalf("password credential metadata returned %v, %v", values, err)
	}
	wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	if values["authorization"] != wantAuthorization {
		t.Fatal("password credentials did not use the expected Basic authorization format")
	}
	token := ObserverToken(username, password)
	if len(token) != 64 || ObserverToken(username, "wrong") == token {
		t.Fatal("observer token was not derived from both node credentials")
	}
	for _, test := range []struct {
		name, user, password string
		authorization, token []string
		passwordOK, tokenOK  bool
	}{
		{"correct", username, password, []string{wantAuthorization}, []string{token}, true, true},
		{"absent", username, password, nil, nil, false, false},
		{"duplicates", username, password, []string{wantAuthorization, wantAuthorization}, []string{token, token}, false, false},
		{"wrong", username, password, []string{"Bearer wrong"}, []string{"wrong"}, false, false},
		{"invalid configured user", "bad:user", password, []string{wantAuthorization}, nil, false, false},
		{"missing configured password", username, "", []string{wantAuthorization}, nil, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"authorization": test.authorization, "token": test.token})
			if got := MatchCredentials(ctx, test.user, test.password); got != test.passwordOK {
				t.Errorf("node credentials accepted = %t, want %t", got, test.passwordOK)
			}
			if got := MatchObserverToken(ctx, token); got != test.tokenOK {
				t.Errorf("observer token accepted = %t, want %t", got, test.tokenOK)
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
	if _, err := ClientTLS(path); err == nil {
		t.Fatal("missing explicit CA was accepted")
	}
	if err := os.WriteFile(path, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if creds, err := ClientTLS(path); err != nil || creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("client with explicit CA = %v, %v", creds, err)
	}
	if creds, err := ClientTLS(""); err != nil || creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("client with system roots = %v, %v", creds, err)
	}
}

func TestPinnedTLSStillChecksThePanelHostname(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	digest := sha256.Sum256(server.Certificate().Raw)
	address, err := ParsePanelAddress("wrong.example:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := address.ClientTLS("", "invalid-fingerprint"); err == nil {
		t.Fatal("malformed fingerprint was accepted")
	}
	transport, err := address.ClientTLS("", hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if conn, _, err := transport.ClientHandshake(t.Context(), "wrong.example:443", raw); conn != nil ||
		!errors.As(err, new(x509.HostnameError)) {
		t.Fatalf("pinned TLS handshake for a different hostname = %v, %v", conn, err)
	}
}
