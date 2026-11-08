package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yagp/internal/config"
	"yagp/pkg/nodegrpc"
)

func TestGeneratedReportCertificatePersistsAndCoversAddress(t *testing.T) {
	for _, host := range []string{"panel.example.test", "127.0.0.1", "2001:db8::1"} {
		t.Run(host, func(t *testing.T) {
			t.Chdir(t.TempDir())
			address, err := nodegrpc.ParsePanelHost(host)
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
				t.Fatalf("generated certificate does not authenticate %q: %v", host, err)
			}
			info, err := os.Stat(reportTLSBundle)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("certificate and private key mode = %o, want 0600", info.Mode().Perm())
			}
			if err = generateReportCertificate(address.Host()); err != nil {
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
	address, _ := nodegrpc.ParsePanelHost("panel.example.test")
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err != nil {
		t.Fatal(err)
	}
	wrongAddress, _ := nodegrpc.ParsePanelHost("other.example.test")
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

func TestReportCertificateRejectsExpiredIdentityWithoutReplacingIt(t *testing.T) {
	t.Chdir(t.TempDir())
	address, err := nodegrpc.ParsePanelHost("panel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		DNSNames:  []string{address.Host()},
		NotBefore: time.Now().Add(-48 * time.Hour),
		NotAfter:  time.Now().Add(-24 * time.Hour),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	expiredBundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	expiredBundle = append(expiredBundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})...)
	if err := persistReportCertificate(expiredBundle); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err == nil || !strings.Contains(err.Error(), "有效期") {
		t.Fatalf("expired certificate = %v", err)
	}
	got, err := os.ReadFile(reportTLSBundle)
	if err != nil || !bytes.Equal(got, expiredBundle) {
		t.Fatalf("expired certificate was replaced: %v", err)
	}
}

func TestReportCertificateRejectsUnusableDataPath(t *testing.T) {
	t.Chdir(t.TempDir())
	address, err := nodegrpc.ParsePanelHost("panel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Dir(reportTLSBundle)
	contents := []byte("existing file where a data directory is expected")
	if err := os.WriteFile(dataPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err == nil {
		t.Fatal("accepted a data path which is a regular file")
	}
	got, err := os.ReadFile(dataPath)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("existing data path was replaced: %v", err)
	}
}

func TestReportCertificateGenerationRecoversAfterMissingDataDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	address, err := nodegrpc.ParsePanelHost("panel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Dir(reportTLSBundle)
	target := filepath.Join(t.TempDir(), "missing-data")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dataPath); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err == nil || !strings.Contains(err.Error(), "生成面板证书") {
		t.Fatalf("certificate generation error = %v", err)
	}
	if _, err := os.Stat(reportTLSBundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation failure left a bundle behind: %v", err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReportCertificate(config.ReportConfig{}, address); err != nil {
		t.Fatalf("certificate did not generate once the data directory was available: %v", err)
	}
}

func TestReportCertificateKeepsExplicitPair(t *testing.T) {
	t.Chdir(t.TempDir())
	address, _ := nodegrpc.ParsePanelHost("panel.example.test")
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
