package gost

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

const (
	tunnelHopName   = "hop-0"
	tunnelRelayName = "exit-relay"
)

// TunnelRelayAddress finds the exit address in a panel-managed chain.
func (c ChainConfig) TunnelRelayAddress() (string, bool) {
	for _, hop := range c.Hops {
		if hop == nil || hop.Name != tunnelHopName {
			continue
		}
		for _, node := range hop.Nodes {
			if node != nil && node.Name == tunnelRelayName {
				return node.Addr, true
			}
		}
		break
	}
	return "", false
}

// UpdateTunnelRelayHost replaces the host while preserving the relay port and chain settings.
func (c *Client) UpdateTunnelRelayHost(chainName, host string) (bool, error) {
	path := "/config/chains/" + chainName
	raw, err := c.resource(path)
	if err != nil || len(raw) == 0 {
		return false, err
	}
	chain, err := patchTunnelRelayHost(raw, host)
	if err != nil || chain == nil {
		return false, err
	}
	resp, err := c.doRequest(http.MethodPut, path, chain)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("更新 Gost 转发链失败: %s", body)
	}
	return true, nil
}

func patchTunnelRelayHost(raw json.RawMessage, host string) (map[string]json.RawMessage, error) {
	// Gost replaces the full chain on PUT. Retain fields beyond our typed config.
	var chain map[string]json.RawMessage
	if err := json.Unmarshal(raw, &chain); err != nil {
		return nil, err
	}
	hops, hopIndex, err := findChainItem(chain["hops"], tunnelHopName)
	if err != nil || hopIndex < 0 {
		return nil, err
	}
	nodes, nodeIndex, err := findChainItem(hops[hopIndex]["nodes"], tunnelRelayName)
	if err != nil || nodeIndex < 0 {
		return nil, err
	}
	node := nodes[nodeIndex]
	if len(node["addr"]) == 0 {
		return nil, nil
	}
	var current string
	if err = json.Unmarshal(node["addr"], &current); err != nil {
		return nil, err
	}
	currentHost, port, err := net.SplitHostPort(current)
	if err != nil || port == "" || currentHost == host {
		return nil, nil
	}
	node["addr"], _ = json.Marshal(net.JoinHostPort(host, port))
	hops[hopIndex]["nodes"], err = json.Marshal(nodes)
	if err != nil {
		return nil, err
	}
	chain["hops"], err = json.Marshal(hops)
	return chain, err
}

func findChainItem(raw json.RawMessage, name string) ([]map[string]json.RawMessage, int, error) {
	if len(raw) == 0 {
		return nil, -1, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, -1, err
	}
	for i, item := range items {
		if len(item["name"]) == 0 {
			continue
		}
		var itemName string
		if err := json.Unmarshal(item["name"], &itemName); err != nil {
			return nil, -1, err
		}
		if itemName == name {
			return items, i, nil
		}
	}
	return nil, -1, nil
}
