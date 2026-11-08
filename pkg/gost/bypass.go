package gost

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

const TransparentBypassName = "yagp-transparent-proxy"

type bypassConfig struct {
	Name      string   `json:"name"`
	Whitelist bool     `json:"whitelist"`
	Matchers  []string `json:"matchers"`
}

func (c *Client) SetTransparentBypass(matchers []string) (bool, error) {
	path := "/config/bypasses/" + TransparentBypassName
	current, err := c.resource(path)
	if err != nil {
		return false, err
	}
	method := http.MethodPost
	if len(current) == 0 {
		path = "/config/bypasses"
	} else {
		var existing bypassConfig
		if err = json.Unmarshal(current, &existing); err != nil {
			return false, err
		}
		if existing.Whitelist && slices.Equal(existing.Matchers, matchers) {
			return false, nil
		}
		method = http.MethodPut
	}
	resp, err := c.doRequest(method, path, bypassConfig{Name: TransparentBypassName, Whitelist: true, Matchers: matchers})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return false, fmt.Errorf("Gost 分流白名单更新失败（HTTP %d）", resp.StatusCode)
	}
	return true, nil
}
