package nodegrpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
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
