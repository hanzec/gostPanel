package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"yagp/internal/dto"
	"yagp/internal/errors"
)

const maxBarkResponseBytes = 4 << 10

var barkHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		// Do not forward the device key to another endpoint.
		return http.ErrUseLastResponse
	},
}

// SendTestBark sends a test notification using the submitted configuration.
func (s *SystemConfigService) SendTestBark(ctx context.Context, config dto.BarkConfig) error {
	server, err := normalizeBarkConfig(&config, false)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(struct {
		DeviceKey string `json:"device_key"`
		Title     string `json:"title"`
		Body      string `json:"body"`
	}{
		DeviceKey: config.DeviceKey,
		Title:     "yagp 测试通知",
		Body:      "这是一条来自 yagp 的 Bark 测试推送。",
	})
	if err != nil {
		return errors.ErrInternal
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.JoinPath("push").String(), bytes.NewReader(payload))
	if err != nil {
		return errors.ErrBarkServerURLInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := barkHTTPClient.Do(req)
	if err != nil {
		return errors.ErrBarkPushFailed
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.ErrBarkPushFailed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBarkResponseBytes+1))
	if err != nil || len(body) > maxBarkResponseBytes {
		return errors.ErrBarkPushFailed
	}
	var result struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Code != http.StatusOK {
		return errors.ErrBarkPushFailed
	}
	return nil
}

func normalizeBarkConfig(config *dto.BarkConfig, allowEmpty bool) (*url.URL, error) {
	config.ServerURL = strings.TrimSpace(config.ServerURL)
	config.DeviceKey = strings.TrimSpace(config.DeviceKey)
	if allowEmpty && config.ServerURL == "" && config.DeviceKey == "" {
		return nil, nil
	}
	if config.ServerURL == "" || config.DeviceKey == "" {
		return nil, errors.ErrBarkConfigIncomplete
	}
	if len(config.DeviceKey) > 255 {
		return nil, errors.ErrBarkDeviceKeyInvalid
	}

	server, err := url.Parse(config.ServerURL)
	if err != nil || len(config.ServerURL) > 2048 || (server.Scheme != "http" && server.Scheme != "https") ||
		server.Hostname() == "" || server.User != nil || server.RawQuery != "" || server.ForceQuery ||
		strings.Contains(config.ServerURL, "#") {
		return nil, errors.ErrBarkServerURLInvalid
	}
	if port := server.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return nil, errors.ErrBarkServerURLInvalid
		}
	}
	return server, nil
}
