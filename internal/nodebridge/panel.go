package nodebridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"yagp/pkg/nodegrpc"

	"gopkg.in/yaml.v3"
)

const (
	PanelCredentialsFile = "panel-credentials.yaml"
	refreshLeadTime      = time.Minute
)

func NewPanelClient(host nodegrpc.PanelHost, caFile, fingerprint string) (*http.Client, error) {
	tlsConfig, err := host.TLSConfig(caFile, fingerprint)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSClientConfig:       tlsConfig,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}, nil
}

type panelCredentials struct {
	AccessToken      string `yaml:"access_token"`
	RefreshToken     string `yaml:"refresh_token"`
	AccessExpiresAt  int64  `yaml:"access_expires_at"`
	RefreshExpiresAt int64  `yaml:"refresh_expires_at"`
	PendingRefreshID string `yaml:"pending_refresh_id,omitempty"`
}

func parsePanelCredentials(reply *nodegrpc.NodeCredentials) (panelCredentials, error) {
	credentials := panelCredentials{
		AccessToken: reply.GetAccessToken(), RefreshToken: reply.GetRefreshToken(),
		AccessExpiresAt: reply.GetAccessExpiresAt(), RefreshExpiresAt: reply.GetRefreshExpiresAt(),
	}
	if !credentials.valid() || credentials.AccessExpiresAt <= time.Now().Unix() {
		return panelCredentials{}, errors.New("panel returned invalid credentials")
	}
	return credentials, nil
}

func (c panelCredentials) valid() bool {
	return c.AccessToken != "" && c.RefreshToken != "" && c.AccessExpiresAt > 0 && c.RefreshExpiresAt > c.AccessExpiresAt
}

func EncodePanelCredentials(reply *nodegrpc.NodeCredentials) ([]byte, error) {
	credentials, err := parsePanelCredentials(reply)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(credentials)
}

func writePanelCredentials(path string, credentials panelCredentials) error {
	data, err := yaml.Marshal(credentials)
	if err != nil {
		return err
	}
	return WriteNodeFile(path, data)
}

// WriteNodeFile atomically replaces one private node state file.
func WriteNodeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".yagp-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

type credentialManager struct {
	mu          sync.Mutex
	credentials panelCredentials
	path        string
	nodeID      uint64
	host        nodegrpc.PanelHost
	client      *http.Client
}

func openCredentialManager(cfg Config, host nodegrpc.PanelHost, client *http.Client) (*credentialManager, error) {
	path := filepath.Join(filepath.Dir(cfg.ConfigFile), PanelCredentialsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read panel credentials: %w", err)
	}
	var credentials panelCredentials
	if err = yaml.Unmarshal(data, &credentials); err != nil || !credentials.valid() {
		return nil, errors.New("invalid stored panel credentials")
	}
	return &credentialManager{credentials: credentials, path: path, nodeID: cfg.NodeID, host: host, client: client}, nil
}

func (m *credentialManager) accessToken(ctx context.Context, lead time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.credentials.PendingRefreshID != "" || m.credentials.AccessExpiresAt <= time.Now().Add(lead).Unix() {
		if err := m.refresh(ctx); err != nil {
			return "", err
		}
	}
	return m.credentials.AccessToken, nil
}

func (m *credentialManager) nextRefresh() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.credentials.PendingRefreshID != "" {
		return 0
	}
	return time.Until(time.Unix(m.credentials.AccessExpiresAt, 0).Add(-refreshLeadTime))
}

// Persist the ID before sending it so retries after a lost reply or restart are identical.
func (m *credentialManager) refresh(ctx context.Context) error {
	if m.credentials.PendingRefreshID == "" {
		if m.credentials.RefreshExpiresAt <= time.Now().Unix() {
			return errors.New("panel refresh token expired; re-enroll the node")
		}
		var nonce [16]byte
		_, _ = rand.Read(nonce[:])
		pending := m.credentials
		pending.PendingRefreshID = hex.EncodeToString(nonce[:])
		if err := writePanelCredentials(m.path, pending); err != nil {
			return fmt.Errorf("persist refresh request: %w", err)
		}
		m.credentials = pending
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request := &nodegrpc.RefreshNodeRequest{
		NodeId: m.nodeID, RefreshToken: m.credentials.RefreshToken, RequestId: m.credentials.PendingRefreshID,
	}
	reply := new(nodegrpc.NodeCredentials)
	if err := nodegrpc.PostProtobuf(ctx, m.client, m.host.HTTPURL(nodegrpc.RefreshPath), request, reply); err != nil {
		var response *nodegrpc.HTTPStatusError
		if errors.As(err, &response) && response.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("panel rejected the refresh token; re-enroll the node: %w", err)
		}
		return fmt.Errorf("refresh panel credentials: %w", err)
	}
	credentials, err := parsePanelCredentials(reply)
	if err != nil {
		return err
	}
	if err = writePanelCredentials(m.path, credentials); err != nil {
		return fmt.Errorf("persist refreshed panel credentials: %w", err)
	}
	m.credentials = credentials
	return nil
}
