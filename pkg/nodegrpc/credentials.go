package nodegrpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

type passwordCredentials struct{ authorization string }

func PasswordCredentials(username, password string) credentials.PerRPCCredentials {
	return passwordCredentials{authorization: authorization(username, password)}
}

func (c passwordCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": c.authorization}, nil
}

func (passwordCredentials) RequireTransportSecurity() bool { return true }

func MatchCredentials(ctx context.Context, username, password string) bool {
	return username != "" && password != "" && !strings.Contains(username, ":") &&
		matchMetadata(ctx, "authorization", authorization(username, password))
}

func ObserverToken(username, password string) string {
	digest := sha256.Sum256([]byte("yagp-gost-observer:" + authorization(username, password)))
	return hex.EncodeToString(digest[:])
}

func MatchObserverToken(ctx context.Context, token string) bool {
	return matchMetadata(ctx, "token", token)
}

func matchMetadata(ctx context.Context, key, expected string) bool {
	values := metadata.ValueFromIncomingContext(ctx, key)
	if len(values) != 1 {
		return false
	}
	valueHash := sha256.Sum256([]byte(values[0]))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(valueHash[:], expectedHash[:]) == 1
}

func authorization(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func ClientTLS(caFile string) (credentials.TransportCredentials, error) {
	if caFile != "" {
		return credentials.NewClientTLSFromFile(caFile, "")
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12}), nil
}

// ClientTLS uses the pinned panel certificate when the panel generated its own TLS identity.
func (a PanelAddress) ClientTLS(caFile, fingerprint string) (credentials.TransportCredentials, error) {
	if fingerprint == "" {
		return ClientTLS(caFile)
	}
	pin, err := decodeCertificateFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: a.Host(),
		// Pin verification uses the matching leaf as the sole trust anchor.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("panel did not present a TLS certificate")
			}
			leaf := state.PeerCertificates[0]
			digest := sha256.Sum256(leaf.Raw)
			if subtle.ConstantTimeCompare(pin, digest[:]) != 1 {
				return errors.New("panel TLS certificate fingerprint mismatch")
			}
			roots := x509.NewCertPool()
			roots.AddCert(leaf)
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: a.Host(), Roots: roots})
			return err
		},
	}), nil
}

func decodeCertificateFingerprint(fingerprint string) ([]byte, error) {
	if len(fingerprint) != hex.EncodedLen(sha256.Size) || fingerprint != strings.ToLower(fingerprint) {
		return nil, errors.New("panel TLS certificate fingerprint must be a lowercase SHA-256 hex digest")
	}
	return hex.DecodeString(fingerprint)
}
