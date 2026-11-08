package gost

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
)

const RelayAllowlistName = "yagp-relay-allowlist"

type AdmissionConfig struct {
	Name      string   `json:"name"`
	Whitelist bool     `json:"whitelist"`
	Matchers  []string `json:"matchers"`
}

func (s ServiceConfig) HasRelayAllowlist() bool {
	return s.Admission == RelayAllowlistName || slices.Contains(s.Admissions, RelayAllowlistName)
}

func (c *Client) EnsureAdmission(desired AdmissionConfig, current []AdmissionConfig) (bool, error) {
	method, path := http.MethodPost, "/config/admissions"
	for _, admission := range current {
		if admission.Name != desired.Name {
			continue
		}
		if admission.Whitelist == desired.Whitelist && slices.Equal(admission.Matchers, desired.Matchers) {
			return false, nil
		}
		method, path = http.MethodPut, path+"/"+desired.Name
		break
	}
	return c.writeAdmissionConfig(method, path, desired)
}

func (c *Client) AttachRelayAllowlist(name string) (bool, error) {
	path := "/config/services/" + name
	raw, err := c.resource(path)
	if err != nil || len(raw) == 0 {
		return false, err
	}
	var service ServiceConfig
	if err = json.Unmarshal(raw, &service); err != nil {
		return false, err
	}
	if service.Handler == nil || service.Handler.Type != "relay" || service.HasRelayAllowlist() {
		return false, nil
	}
	// Retain unrelated Gost fields and any admission rules already on this relay.
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return false, err
	}
	fields["admissions"], err = json.Marshal(append(service.Admissions, RelayAllowlistName))
	if err != nil {
		return false, err
	}
	delete(fields, "status")
	return c.writeAdmissionConfig(http.MethodPut, path, fields)
}

func (c *Client) writeAdmissionConfig(method, path string, payload any) (bool, error) {
	resp, err := c.doRequest(method, path, payload)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("写入 Gost 准入配置失败: %s", body)
	}
	return true, nil
}
