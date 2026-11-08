package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"yagp/internal/config"
	"yagp/pkg/nodegrpc"
)

func TestGeneratedReportCertificatePersistsAndCoversAddress(t *testing.T) {
	for _, endpoint := range []string{"panel.example.test:39101", "127.0.0.1:39101", "[2001:db8::1]:39101"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Chdir(t.TempDir())
			address, err := nodegrpc.ParsePanelAddress(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			first, err := loadReportCertificate(config.ReportConfig{}, address)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := x509.ParseCertificate(first.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(leaf)
			if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: address.Host()}); err != nil {
				t.Fatalf("generated certificate does not authenticate %q: %v", endpoint, err)
			}
			info, err := os.Stat(reportTLSBundle)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("certificate and private key mode = %o, want 0600", info.Mode().Perm())
			}
			if err = generateReportCertificate(address.Host()); !errors.Is(err, os.ErrExist) {
				t.Fatalf("attempted replacement of generated certificate = %v", err)
			}
			second, err := loadReportCertificate(config.ReportConfig{}, address)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
				t.Fatal("reloading changed the certificate")
			}
			digest := sha256.Sum256(first.Certificate[0])
			if got := reportCertificateFingerprint(second); got != hex.EncodeToString(digest[:]) {
				t.Fatalf("fingerprint = %q", got)
			}
		})
	}
}

func TestReportCertificateRejectsInvalidStoredIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	address, _ := nodegrpc.ParsePanelAddress("panel.example.test:39101")
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err != nil {
		t.Fatal(err)
	}
	wrongAddress, _ := nodegrpc.ParsePanelAddress("other.example.test:39101")
	if _, err := loadReportCertificate(config.ReportConfig{}, wrongAddress); err == nil || !strings.Contains(err.Error(), wrongAddress.Host()) {
		t.Fatalf("hostname mismatch = %v", err)
	}
	contents := []byte("damaged certificate file")
	if err := os.WriteFile(reportTLSBundle, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err == nil {
		t.Fatal("accepted a damaged certificate")
	}
	got, err := os.ReadFile(reportTLSBundle)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("damaged certificate was silently replaced: %v", err)
	}
}

func TestReportCertificateKeepsExplicitPair(t *testing.T) {
	t.Chdir(t.TempDir())
	address, _ := nodegrpc.ParsePanelAddress("panel.example.test:39101")
	generated, err := loadReportCertificate(config.ReportConfig{}, address)
	if err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "custom.pem")
	if err = os.Rename(reportTLSBundle, explicit); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadReportCertificate(config.ReportConfig{CertFile: explicit, KeyFile: explicit}, address)
	if err != nil || !bytes.Equal(loaded.Certificate[0], generated.Certificate[0]) {
		t.Fatalf("explicit certificate was not loaded: %v", err)
	}
	if _, err = loadReportCertificate(config.ReportConfig{CertFile: explicit}, address); err == nil || !strings.Contains(err.Error(), "report.key_file") {
		t.Fatalf("missing explicit key = %v", err)
	}
	if _, err = os.Stat(reportTLSBundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit pair produced a generated certificate: %v", err)
	}
}
