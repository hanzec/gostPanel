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
	"os"
	"strings"

	"google.golang.org/grpc/metadata"
)

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

// TLSConfig authenticates the panel for HTTPS and WebSocket requests.
func (h PanelHost) TLSConfig(caFile, fingerprint string) (*tls.Config, error) {
	if fingerprint == "" {
		config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: h.Host()}
		if caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return nil, err
			}
			config.RootCAs = x509.NewCertPool()
			if !config.RootCAs.AppendCertsFromPEM(pem) {
				return nil, errors.New("invalid panel CA certificate")
			}
		}
		return config, nil
	}
	pin, err := decodeCertificateFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: h.Host(),
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
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: h.Host(), Roots: roots})
			return err
		},
	}, nil
}

func decodeCertificateFingerprint(fingerprint string) ([]byte, error) {
	if len(fingerprint) != hex.EncodedLen(sha256.Size) || fingerprint != strings.ToLower(fingerprint) {
		return nil, errors.New("panel TLS certificate fingerprint must be a lowercase SHA-256 hex digest")
	}
	return hex.DecodeString(fingerprint)
}
