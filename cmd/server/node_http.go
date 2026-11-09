package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"yagp/internal/config"
	"yagp/internal/handler"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"gorm.io/gorm"
)

const nodeTokenKeyPath = "data/node-token.key"

type nodeHTTPServer struct {
	server   *http.Server
	listener net.Listener
	cancel   context.CancelFunc
}

func (s *nodeHTTPServer) Addr() net.Addr { return s.listener.Addr() }

func (s *nodeHTTPServer) Close() error {
	s.cancel()
	return s.server.Close()
}

func startReports(cfg config.ReportConfig, host string, db *gorm.DB) (*nodeHTTPServer, string, error) {
	if cfg.Listen == "" {
		return nil, "", nil
	}
	panel, err := nodegrpc.ParsePanelHost(host)
	if err != nil {
		return nil, "", fmt.Errorf("host: %w", err)
	}
	var tlsConfig *tls.Config
	var fingerprint string
	if cfg.DirectTLS {
		certificate, err := loadReportCertificate(cfg, panel)
		if err != nil {
			return nil, "", err
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}, NextProtos: []string{"http/1.1"}}
		if cfg.CertFile == "" && cfg.KeyFile == "" {
			fingerprint = reportCertificateFingerprint(certificate)
		}
	}
	key, err := loadNodeTokenKey()
	if err != nil {
		return nil, "", fmt.Errorf("加载节点凭据密钥: %w", err)
	}
	reports := handler.NewReportHandler(db, !cfg.DirectTLS)
	if err := reports.SetNodeTokenKey(key); err != nil {
		return nil, "", err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, "", err
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+nodegrpc.RegisterPath, reports.RegisterHTTP)
	mux.HandleFunc("POST "+nodegrpc.RefreshPath, reports.RefreshHTTP)
	mux.HandleFunc("GET "+nodegrpc.SessionPath, reports.SessionHTTP)
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{
		Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		logger.Infof("节点会话服务监听 %s", listener.Addr())
		if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("节点会话服务失败: %v", err)
		}
	}()
	return &nodeHTTPServer{server: server, listener: listener, cancel: cancel}, fingerprint, nil
}

func loadNodeTokenKey() ([]byte, error) {
	key, err := os.ReadFile(nodeTokenKeyPath)
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	key = make([]byte, 32)
	rand.Read(key)
	if err := persistReportSecret(nodeTokenKeyPath, key); err != nil {
		return nil, err
	}
	return os.ReadFile(nodeTokenKeyPath)
}
