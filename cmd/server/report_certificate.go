package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"yagp/internal/config"
	"yagp/pkg/nodegrpc"
)

const reportTLSBundle = "data/report-tls.pem"

func loadReportCertificate(cfg config.ReportConfig, address nodegrpc.PanelHost) (tls.Certificate, error) {
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return tls.Certificate{}, errors.New("report.cert_file 和 report.key_file 必须同时设置或留空")
		}
		return tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	}

	bundle, err := os.ReadFile(reportTLSBundle)
	if errors.Is(err, os.ErrNotExist) {
		if err = generateReportCertificate(address.Host()); err != nil {
			return tls.Certificate{}, fmt.Errorf("生成面板证书 %s: %w", reportTLSBundle, err)
		}
		bundle, err = os.ReadFile(reportTLSBundle)
	}
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("读取面板证书 %s: %w", reportTLSBundle, err)
	}
	cert, err := tls.X509KeyPair(bundle, bundle)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("加载面板证书 %s: %w", reportTLSBundle, err)
	}
	if err = checkGeneratedReportCertificate(cert, address.Host()); err != nil {
		return tls.Certificate{}, fmt.Errorf("检查面板证书 %s: %w", reportTLSBundle, err)
	}
	return cert, nil
}

func checkGeneratedReportCertificate(cert tls.Certificate, host string) error {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	if err = leaf.VerifyHostname(host); err != nil {
		return err
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return errors.New("自动生成的证书不在有效期内")
	}
	return nil
}

func generateReportCertificate(host string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(5, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})...)
	return persistReportCertificate(bundle)
}

func persistReportCertificate(bundle []byte) error {
	return persistReportSecret(reportTLSBundle, bundle)
}

func persistReportSecret(path string, secret []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".report-secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(secret)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	// Publish the complete file without replacing an identity another instance has installed.
	err = os.Link(file.Name(), path)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

func reportCertificateFingerprint(cert tls.Certificate) string {
	digest := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(digest[:])
}
