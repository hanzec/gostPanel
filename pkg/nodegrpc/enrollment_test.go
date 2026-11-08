package nodegrpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
)

func TestParseRegistrationTokenID(t *testing.T) {
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, test := range []struct {
		token string
		id    uint64
	}{
		{"1." + secret, 1},
		{"4294967295." + secret, 4294967295},
		{"4294967296." + secret, 0},
		{"0." + secret, 0},
		{"1." + secret[:42] + "B", 0},
		{"1." + secret[:42] + "?", 0},
		{"1." + secret + "=", 0},
		{"1." + secret[:42], 0},
		{"1", 0},
	} {
		t.Run(test.token, func(t *testing.T) {
			id, err := ParseRegistrationTokenID(test.token)
			if id != test.id || (err != nil) != (test.id == 0) {
				t.Fatalf("parsed ID = %d, %v, want %d", id, err, test.id)
			}
		})
	}
}

func TestNodeCertificateEnrollmentValidation(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name   string
		change func(*x509.Certificate)
		forged bool
	}{
		{name: "self-signed leaf"},
		{name: "authority", change: func(cert *x509.Certificate) { cert.IsCA = true }},
		{name: "missing basic constraints", change: func(cert *x509.Certificate) { cert.BasicConstraintsValid = false }},
		{name: "wrong DNS name", change: func(cert *x509.Certificate) { cert.DNSNames = []string{"other-node"} }},
		{name: "expired", change: func(cert *x509.Certificate) {
			cert.NotBefore, cert.NotAfter = now.Add(-2*time.Hour), now.Add(-time.Hour)
		}},
		{name: "client only", change: func(cert *x509.Certificate) {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}},
		{name: "forged self-signature", forged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, cert := testNodeCertificate(t, test.change, test.forged)
			if err := ValidateNodeCertificate("\n" + cert + "\n"); (err != nil) != (test.change != nil || test.forged) {
				t.Fatalf("certificate validation returned %v", err)
			}
		})
	}
	_, leaf := testNodeCertificate(t, nil, false)
	for _, malformed := range []string{
		"not PEM",
		leaf + leaf,
		"preamble " + leaf,
		leaf + " trailing text",
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"X-Test": "header"}, Bytes: []byte("not DER")})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})),
	} {
		if err := ValidateNodeCertificate(malformed); err == nil {
			t.Fatal("enrollment accepted malformed certificate input")
		}
		if _, err := NodeClientTLS(malformed); err == nil {
			t.Fatal("node client accepted malformed certificate input")
		}
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
	_, cert := testNodeCertificate(t, nil, false)
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-ca.pem")
	if _, err := ClientTLS(path); err == nil {
		t.Fatal("missing explicit CA was accepted")
	}
	if err := os.WriteFile(path, []byte(cert), 0600); err != nil {
		t.Fatal(err)
	}
	if creds, err := ClientTLS(path); err != nil || creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("client with explicit CA = %v, %v", creds, err)
	}
	if creds, err := ClientTLS(""); err != nil || creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("client with system roots = %v, %v", creds, err)
	}
}

func testNodeCertificate(t *testing.T, change func(*x509.Certificate), forged bool) (tls.Certificate, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: NodeCertificateName},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{NodeCertificateName}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		BasicConstraintsValid: true, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if change != nil {
		change(&cert)
	}
	issuer := cert
	var signer = privateKey
	if forged {
		_, signer, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &cert, &issuer, publicKey, signer)
	if err != nil {
		t.Fatal(fmt.Errorf("create fixture certificate: %w", err))
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey}, strings.TrimSpace(string(encoded))
}
