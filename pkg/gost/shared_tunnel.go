package gost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (c *Client) Service(name string) (*ServiceConfig, error) {
	raw, err := c.resource("/config/services/" + name)
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var service ServiceConfig
	if err := json.Unmarshal(raw, &service); err != nil {
		return nil, err
	}
	return &service, nil
}

// ReplaceService restores the prior listener if Gost closes it before a failed replacement.
func (c *Client) ReplaceService(service *ServiceConfig) error {
	path := "/config/services/" + service.Name
	raw, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		_, err = c.EnsureService(service)
		return err
	}
	if err := c.writeTunnelResource(http.MethodPut, path, service); err != nil {
		return errors.Join(err, c.writeTunnelResource(http.MethodPut, path, raw))
	}
	return nil
}

func (c *Client) EnsureRelayAuther(name, address, token string) error {
	path := "/config/authers/" + name
	raw, err := c.resource(path)
	if err != nil || len(raw) != 0 {
		return err
	}
	return c.writeTunnelResource(http.MethodPost, "/config/authers", struct {
		Name   string        `json:"name"`
		Plugin *PluginConfig `json:"plugin"`
	}{name, &PluginConfig{Type: "grpc", Addr: address, Token: token}})
}

func (c *Client) ConfigureRelayAuth(name string, policy nodegrpc.RelayAuthConfig) error {
	path := nodegrpc.RelayAuthPathPrefix + name
	resp, err := c.doRequestContext(context.Background(), http.MethodPut, path, policy)
	if err != nil {
		if status.Code(err) == codes.InvalidArgument || status.Code(err) == codes.Unimplemented {
			return status.Error(codes.Unimplemented, "shared Relay authentication unavailable")
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("更新共享 Relay 认证失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}

func (c *Client) RelayAuthConfigured(name string) (bool, error) {
	resp, err := c.doRequest(http.MethodGet, nodegrpc.RelayAuthPathPrefix+name, nil)
	if status.Code(err) == codes.InvalidArgument || status.Code(err) == codes.Unimplemented {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("查询共享 Relay 认证失败（HTTP %d）", resp.StatusCode)
	}
	var result nodegrpc.RelayAuthStatus
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result.Configured, err
}

func (c *Client) DeleteRelayAuther(name string) error {
	path := "/config/authers/" + name
	raw, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(raw) != 0 {
		if err := c.writeTunnelResource(http.MethodDelete, path, nil); err != nil {
			return err
		}
	}
	return c.writeTunnelResource(http.MethodDelete, nodegrpc.RelayAuthPathPrefix+name, nil)
}

func (c *Client) writeTunnelResource(method, path string, config any) error {
	resp, err := c.doRequest(method, path, config)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("更新 Gost 隧道资源失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}
