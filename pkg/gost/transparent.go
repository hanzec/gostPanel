package gost

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ClashRedirectMark exempts Gost's outbound sockets from the local redirect.
const ClashRedirectMark = 0x59414750

func BuildClashRedirectService(name string) *ServiceConfig {
	return &ServiceConfig{
		Name:     name,
		Listener: &ListenerConfig{Type: "red"},
		Handler: &HandlerConfig{
			Type:     "red",
			Metadata: map[string]any{"sniffing": true, "sniffing.fallback": true, "sniffing.timeout": "5s"},
		},
		SockOpts: &SocketOptions{Mark: ClashRedirectMark},
	}
}

// EnsureClashChain leaves the fixed-target tunnel's route and configuration intact.
func (c *Client) EnsureClashChain(source, name string) error {
	current, err := c.resource("/config/chains/" + name)
	if err != nil || len(current) != 0 {
		return err
	}
	raw, err := c.resource("/config/chains/" + source)
	if err != nil {
		return err
	}
	chain, err := clashChain(raw, name)
	if err != nil {
		return err
	}
	resp, err := c.doRequest(http.MethodPost, "/config/chains", chain)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("创建 Gost 透明分流链失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}

func clashChain(raw json.RawMessage, name string) (map[string]json.RawMessage, error) {
	var chain map[string]json.RawMessage
	if err := json.Unmarshal(raw, &chain); err != nil {
		return nil, err
	}
	hops, first, err := findChainItem(chain["hops"], tunnelHopName)
	if err != nil || first != 0 {
		return nil, fmt.Errorf("Gost 隧道缺少面板管理的首跳")
	}
	hop := hops[first]
	nodes, exit, err := findChainItem(hop["nodes"], tunnelRelayName)
	if err != nil || exit < 0 {
		return nil, fmt.Errorf("Gost 隧道缺少出口 Relay")
	}
	// A whitelist on the first hop sends unmatched destinations directly.
	hop["bypass"], _ = json.Marshal(TransparentBypassName)
	sockOpts, _ := json.Marshal(SocketOptions{Mark: ClashRedirectMark})
	hop["sockopts"] = sockOpts
	nodes[exit]["sockopts"] = sockOpts
	hop["nodes"], err = json.Marshal(nodes)
	if err != nil {
		return nil, err
	}
	chain["name"], _ = json.Marshal(name)
	chain["hops"], err = json.Marshal(hops)
	return chain, err
}
