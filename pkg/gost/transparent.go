package gost

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ClashRedirectMark exempts Gost's outbound sockets from the local redirect.
const ClashRedirectMark = 0x59414750

func BuildClashRedirectService(name string) *ServiceConfig {
	return BuildMarkedClashRedirectService(name, ClashRedirectMark)
}

func BuildMarkedClashRedirectService(name string, mark uint32) *ServiceConfig {
	return &ServiceConfig{
		Name:     name,
		Listener: &ListenerConfig{Type: "red"},
		Handler: &HandlerConfig{
			Type:     "red",
			Metadata: map[string]any{"sniffing": true, "sniffing.fallback": true, "sniffing.timeout": "5s"},
		},
		SockOpts: &SocketOptions{Mark: int(mark)},
	}
}

// EnsureClashChain leaves the fixed-target tunnel's route and configuration intact.
func (c *Client) EnsureClashChain(source, name string) error {
	return c.ensureClashChain(source, name, ClashRedirectMark, false)
}

func (c *Client) EnsureMarkedClashChain(source, name string, mark uint32) error {
	return c.ensureClashChain(source, name, mark, true)
}

func (c *Client) ensureClashChain(source, name string, mark uint32, replace bool) error {
	current, err := c.resource("/config/chains/" + name)
	if err != nil || (!replace && len(current) != 0) {
		return err
	}
	raw, err := c.resource("/config/chains/" + source)
	if err != nil {
		return err
	}
	chain, err := markedClashChain(raw, name, mark)
	if err != nil {
		return err
	}
	method, path := http.MethodPost, "/config/chains"
	if len(current) != 0 {
		method, path = http.MethodPut, path+"/"+name
	}
	resp, err := c.doRequest(method, path, chain)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("创建 Gost 透明分流链失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}

func markedClashChain(raw json.RawMessage, name string, mark uint32) (map[string]json.RawMessage, error) {
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
	sockOpts, _ := json.Marshal(SocketOptions{Mark: int(mark)})
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
