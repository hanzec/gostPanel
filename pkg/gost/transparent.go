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
	return BuildMarkedTransparentRedirectService(name, mark, true)
}

// BuildMarkedTransparentRedirectService forwards to the original socket destination.
// Domain-based routing also reads HTTP Host or TLS SNI to match the configured rules.
func BuildMarkedTransparentRedirectService(name string, mark uint32, matchDomains bool) *ServiceConfig {
	service := &ServiceConfig{
		Name:     name,
		Listener: &ListenerConfig{Type: "red"},
		Handler:  &HandlerConfig{Type: "red"},
		SockOpts: &SocketOptions{Mark: int(mark)},
	}
	if matchDomains {
		service.Handler.Metadata = map[string]any{"sniffing": true, "sniffing.fallback": true, "sniffing.timeout": "5s"}
	}
	return service
}

// EnsureClashChain leaves the fixed-target tunnel's route and configuration intact.
func (c *Client) EnsureClashChain(source, name string) error {
	return c.ensureTransparentChain(source, name, ClashRedirectMark, TransparentBypassName, false)
}

func (c *Client) EnsureMarkedClashChain(source, name string, mark uint32) error {
	return c.EnsureMarkedTransparentChain(source, name, mark, TransparentBypassName)
}

// EnsureMarkedTransparentChain clones the Relay route with the redirect exemption mark.
// An empty bypass sends every captured connection through the tunnel.
func (c *Client) EnsureMarkedTransparentChain(source, name string, mark uint32, bypass string) error {
	return c.ensureTransparentChain(source, name, mark, bypass, true)
}

func (c *Client) ensureTransparentChain(source, name string, mark uint32, bypass string, replace bool) error {
	current, err := c.resource("/config/chains/" + name)
	if err != nil || (!replace && len(current) != 0) {
		return err
	}
	raw, err := c.resource("/config/chains/" + source)
	if err != nil {
		return err
	}
	chain, err := markedTransparentChain(raw, name, mark, bypass)
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

func markedTransparentChain(raw json.RawMessage, name string, mark uint32, bypass string) (map[string]json.RawMessage, error) {
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
	if bypass == "" {
		delete(hop, "bypass")
	} else {
		hop["bypass"], _ = json.Marshal(bypass)
	}
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
