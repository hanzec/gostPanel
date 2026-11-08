package nodegrpc

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strconv"
	"strings"

	"google.golang.org/grpc/credentials"
)

// NodeCertificateName is the stable DNS SAN used to verify enrolled control server certificates.
const NodeCertificateName = "yagp-node"

func ParseRegistrationTokenID(token string) (uint64, error) {
	idPart, secret, _ := strings.Cut(token, ".")
	id, err := strconv.ParseUint(idPart, 10, 32)
	if err != nil || id == 0 || len(secret) != base64.RawURLEncoding.EncodedLen(32) {
		return 0, errors.New("invalid registration token")
	}
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret); err != nil || len(decoded) != 32 {
		return 0, errors.New("invalid registration token")
	}
	return id, nil
}

func ValidateNodeCertificate(certPEM string) error {
	cert, err := parseNodeCertificate(certPEM)
	if err != nil {
		return err
	}
	// Trust this certificate as a leaf, so it cannot authorize another server certificate.
	if !cert.BasicConstraintsValid || cert.IsCA || !bytes.Equal(cert.RawIssuer, cert.RawSubject) {
		return errors.New("node certificate must be a self-signed leaf")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	_, err = cert.Verify(x509.VerifyOptions{DNSName: NodeCertificateName, Roots: roots})
	return err
}

func NodeClientTLS(certPEM string) (credentials.TransportCredentials, error) {
	cert, err := parseNodeCertificate(certPEM)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: NodeCertificateName,
	}), nil
}

func parseNodeCertificate(certPEM string) (*x509.Certificate, error) {
	certPEM = strings.TrimSpace(certPEM)
	block, rest := pem.Decode([]byte(certPEM))
	if !strings.HasPrefix(certPEM, "-----BEGIN CERTIFICATE-----") || block == nil ||
		block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
		return nil, errors.New("expected one PEM-encoded node certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}
