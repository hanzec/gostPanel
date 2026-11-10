package gost

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
)

const LegacyRelayAllowlistName = "yagp-relay-allowlist"

type AdmissionConfig struct {
	Name      string   `json:"name"`
	Whitelist bool     `json:"whitelist"`
	Matchers  []string `json:"matchers"`
}

func TunnelRelayAllowlistName(relayName string) string {
	return "yagp-" + relayName + "-allowlist"
}

func (s ServiceConfig) HasAdmission(name string) bool {
	return s.Admission == name || slices.Contains(s.Admissions, name)
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

// SetRelayAllowlist replaces the panel's legacy or per-tunnel binding in one service update.
func (c *Client) SetRelayAllowlist(name string, enabled bool) (bool, error) {
	path := "/config/services/" + name
	raw, err := c.resource(path)
	if err != nil || len(raw) == 0 {
		return false, err
	}
	var service ServiceConfig
	if err = json.Unmarshal(raw, &service); err != nil {
		return false, err
	}
	admission := TunnelRelayAllowlistName(name)
	if service.Handler == nil || service.Handler.Type != "relay" ||
		(!service.HasAdmission(LegacyRelayAllowlistName) && service.HasAdmission(admission) == enabled) {
		return false, nil
	}
	// Retain unrelated Gost fields and any admission rules already on this relay.
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return false, err
	}
	if service.Admission == LegacyRelayAllowlistName || service.Admission == admission {
		delete(fields, "admission")
	}
	admissions := slices.DeleteFunc(service.Admissions, func(name string) bool {
		return name == LegacyRelayAllowlistName || name == admission
	})
	if enabled {
		admissions = append(admissions, admission)
	}
	if len(admissions) == 0 {
		delete(fields, "admissions")
	} else {
		fields["admissions"], err = json.Marshal(admissions)
		if err != nil {
			return false, err
		}
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
