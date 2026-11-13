package gost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"yagp/internal/errors"

	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

// Client 通过 gRPC 管理 Gost 节点。
type Client struct {
	config Config
}

// Config 客户端配置
type Config struct {
	NodeID   uint
	Username string
	Password string
	Timeout  time.Duration
}

// NewClient 创建 Gost 客户端
func NewClient(cfg *Config) *Client {
	config := *cfg
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}
	return &Client{config: config}
}

// ServiceConfig 服务配置
type ServiceConfig struct {
	Name       string           `json:"name"`
	Addr       string           `json:"addr"`
	Admission  string           `json:"admission,omitempty"`
	Admissions []string         `json:"admissions,omitempty"`
	Handler    *HandlerConfig   `json:"handler,omitempty"`
	Listener   *ListenerConfig  `json:"listener,omitempty"`
	Forwarder  *ForwarderConfig `json:"forwarder,omitempty"`
	Limiter    string           `json:"limiter,omitempty"`  // 流量速率限制器名称
	CLimiter   string           `json:"climiter,omitempty"` // 并发连接数限制器名称
	RLimiter   string           `json:"rlimiter,omitempty"` // 请求速率限制器名称
	Observer   string           `json:"observer,omitempty"` // 观察器名称
	SockOpts   *SocketOptions   `json:"sockopts,omitempty"`
	Metadata   map[string]any   `json:"metadata,omitempty"` // 元数据配置
	Status     *ServiceStatus   `json:"status,omitempty"`   // 服务运行状态
}

func (s ServiceConfig) IsManagedForwarder() bool {
	if s.Handler == nil {
		return false
	}
	var prefix string
	switch s.Handler.Type {
	case "forward", "red":
		prefix = "rule-"
	case "relay":
		prefix = "relay-tunnel-"
	default:
		return false
	}
	id, ok := strings.CutPrefix(s.Name, prefix)
	value, err := strconv.ParseUint(id, 10, 64)
	return ok && err == nil && value != 0
}

type SocketOptions struct {
	Mark int `json:"mark"`
}

// ServiceStatus 服务运行时状态信息
type ServiceStatus struct {
	State      string               `json:"state"` // 状态: ready, running, failed, closed.
	CreateTime int64                `json:"createTime,omitempty"`
	Stats      *ServiceTrafficStats `json:"stats,omitempty"`
}

type ServiceTrafficStats struct {
	InputBytes  uint64 `json:"inputBytes"`
	OutputBytes uint64 `json:"outputBytes"`
}

// GostResponse 通用 API 响应包裹
type GostResponse struct {
	Data json.RawMessage `json:"data"`
}

// HandlerConfig 处理器配置
type HandlerConfig struct {
	Type       string            `json:"type"`
	Chain      string            `json:"chain,omitempty"` // 链名称
	ChainGroup *ChainGroupConfig `json:"chainGroup,omitempty"`
	Auther     string            `json:"auther,omitempty"`
	Auth       *AuthConfig       `json:"auth,omitempty"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
}

type ChainGroupConfig struct {
	Chains   []ChainGroupEntry `json:"chains"`
	Selector *SelectorConfig   `json:"selector,omitempty"`
}

type ChainGroupEntry struct {
	Chain   string        `json:"chain"`
	Matcher *ChainMatcher `json:"matcher,omitempty"`
}

type ChainMatcher struct {
	Rule string `json:"rule"`
}

// ListenerConfig 监听器配置
type ListenerConfig struct {
	Type     string         `json:"type"`
	Metadata map[string]any `json:"metadata,omitempty"` // 元数据配置
	TLS      map[string]any `json:"tls,omitempty"`
}

// ForwarderConfig 转发器配置
type ForwarderConfig struct {
	Nodes    []*ForwarderNode `json:"nodes"`
	Selector *SelectorConfig  `json:"selector,omitempty"`
}

// SelectorConfig 选择器配置
type SelectorConfig struct {
	Strategy    string        `json:"strategy,omitempty"`
	MaxFails    int           `json:"maxFails,omitempty"`
	FailTimeout time.Duration `json:"failTimeout,omitempty"`
}

// ForwarderNode 转发目标节点
type ForwarderNode struct {
	Name    string `json:"name"`
	Addr    string `json:"addr"`
	Network string `json:"network,omitempty"`
}

// AuthConfig 认证配置
type AuthConfig struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// ChainConfig 链配置
type ChainConfig struct {
	Name string       `json:"name"`
	Hops []*HopConfig `json:"hops,omitempty"`
}

// HopConfig 跳配置
type HopConfig struct {
	Name  string        `json:"name"`
	Nodes []*NodeConfig `json:"nodes,omitempty"`
}

// NodeConfig 链节点配置
type NodeConfig struct {
	Name      string           `json:"name"`
	Addr      string           `json:"addr"`
	Connector *ConnectorConfig `json:"connector,omitempty"`
	Dialer    *DialerConfig    `json:"dialer,omitempty"`
}

// ConnectorConfig 连接器配置
type ConnectorConfig struct {
	Type string      `json:"type"`
	Auth *AuthConfig `json:"auth,omitempty"`
}

// DialerConfig 拨号器配置
type DialerConfig struct {
	Type     string         `json:"type"`
	Metadata map[string]any `json:"metadata,omitempty"`
	TLS      map[string]any `json:"tls,omitempty"`
}

// LimiterConfig 流量速率限制器配置
// limits 格式: "$ 100MB 100MB" (服务级别 入站 出站)
// limits 格式: "$$ 10MB" (连接级别)
// limits 格式: "192.168.1.1 1MB 5MB" (IP级别)
type LimiterConfig struct {
	Name   string        `json:"name"`
	Limits []string      `json:"limits,omitempty"` // 限制规则列表
	Plugin *PluginConfig `json:"plugin,omitempty"` // 插件配置
}

// CLimiterConfig 并发连接数限制器配置
// limits 格式: "$ 1000" (服务级别最大连接数)
// limits 格式: "$$ 100" (IP级别默认最大连接数)
type CLimiterConfig struct {
	Name   string        `json:"name"`
	Limits []string      `json:"limits,omitempty"` // 限制规则列表
	Plugin *PluginConfig `json:"plugin,omitempty"` // 插件配置
}

// RLimiterConfig 请求速率限制器配置
// limits 格式: "$ 100" (服务级别每秒请求数)
// limits 格式: "$$ 10" (IP级别默认每秒请求数)
type RLimiterConfig struct {
	Name   string        `json:"name"`
	Limits []string      `json:"limits,omitempty"` // 限制规则列表
	Plugin *PluginConfig `json:"plugin,omitempty"` // 插件配置
}

// ObserverConfig 观察器配置
type ObserverConfig struct {
	Name   string        `json:"name"`
	Plugin *PluginConfig `json:"plugin,omitempty"` // 插件配置
}

// PluginConfig 插件配置
type PluginConfig struct {
	Type  string `json:"type,omitempty"`  // 插件类型
	Addr  string `json:"addr,omitempty"`  // 插件地址
	Token string `json:"token,omitempty"` // gRPC 插件令牌
}

// HealthCheck 健康检查
func (c *Client) HealthCheck() error {
	resp, err := c.doRequest("GET", "/config", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查失败，状态码: %d", resp.StatusCode)
	}

	return nil
}

// GostConfig Gost 完整配置
type GostConfig struct {
	Services   []ServiceConfig   `json:"services"`
	Admissions []AdmissionConfig `json:"admissions"`
	Chains     []ChainConfig     `json:"chains"`
	Limiters   []LimiterConfig   `json:"limiters"`
	CLimiters  []CLimiterConfig  `json:"climiters"`
	RLimiters  []RLimiterConfig  `json:"rlimiters"`
	Observers  []ObserverConfig  `json:"observers"`
}

// GetConfig 获取节点配置
func (c *Client) GetConfig() (*GostConfig, error) {
	return c.GetConfigContext(context.Background())
}

func (c *Client) GetConfigContext(ctx context.Context) (*GostConfig, error) {
	resp, err := c.doRequestContext(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("获取配置失败: %s", string(body))
	}

	var config GostConfig
	if err = json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("解析配置失败: %v", err)
	}

	return &config, nil
}

// SaveConfig 保存节点配置
func (c *Client) SaveConfig() error {
	resp, err := c.doRequest("POST", "/config?format=yaml", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("保存配置失败: %s", string(body))
	}

	return nil
}

// resource 获取资源数据，空数据表示资源不存在。
func (c *Client) resource(path string) (json.RawMessage, error) {
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查询 Gost 资源失败，状态码: %d", resp.StatusCode)
	}

	var gResp GostResponse
	if err = json.NewDecoder(resp.Body).Decode(&gResp); err != nil {
		return nil, err
	}
	if string(gResp.Data) == "null" {
		return nil, nil
	}
	return gResp.Data, nil
}

// EnsureService creates the service if absent and reports whether it was created.
func (c *Client) EnsureService(svc *ServiceConfig) (bool, error) {
	path := fmt.Sprintf("/config/services/%s", svc.Name)
	existing, err := c.resource(path)
	if err != nil {
		return false, err
	}
	if len(existing) > 0 {
		logger.Debugf("服务 %s 已存在，跳过创建", svc.Name)
		return false, nil
	}

	resp, err := c.doRequest("POST", "/config/services", svc)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("创建服务失败: %s", string(body))
	}

	return true, nil
}

// DeleteService 删除服务 (幂等)
func (c *Client) DeleteService(name string) error {
	path := fmt.Sprintf("/config/services/%s", name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		logger.Debugf("服务 %s 不存在，跳过删除", name)
		return nil
	}

	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除服务失败: %s", string(body))
	}

	return nil
}

// CreateChain 创建链 (幂等)
func (c *Client) CreateChain(chain *ChainConfig) error {
	path := fmt.Sprintf("/config/chains/%s", chain.Name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Debugf("链 %s 已存在，跳过创建", chain.Name)
		return nil
	}

	resp, err := c.doRequest("POST", "/config/chains", chain)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("创建链失败: %s", string(body))
	}

	return nil
}

// DeleteChain 删除链 (幂等)
func (c *Client) DeleteChain(name string) error {
	path := fmt.Sprintf("/config/chains/%s", name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		logger.Debugf("链 %s 不存在，跳过删除", name)
		return nil
	}

	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除链失败: %s", string(body))
	}

	return nil
}

// ObserverAddress 获取节点本机供 Gost 使用的 Observer 地址。
func (c *Client) ObserverAddress() (string, error) {
	client, err := c.connect()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.config.Timeout)
	defer cancel()
	reply, err := client.GetObserverEndpoint(ctx, &emptypb.Empty{})
	if err != nil {
		return "", err
	}
	return reply.GetAddress(), nil
}

func (c *Client) ObserverToken() string {
	return nodegrpc.ObserverToken(c.config.Username, c.config.Password)
}

// doRequest 将 Gost 管理命令交给节点的 gRPC 桥接进程。
func (c *Client) doRequest(method, path string, body interface{}) (*http.Response, error) {
	return c.doRequestContext(context.Background(), method, path, body)
}

func (c *Client) doRequestContext(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	request := &nodegrpc.CommandRequest{Method: method, Path: path}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		request.Body = data
	}

	reply, err := callNode(ctx, c, c.config.Timeout, func(ctx context.Context, client nodegrpc.V1Client) (*nodegrpc.CommandReply, error) {
		return client.Execute(ctx, request)
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: int(reply.GetStatusCode()), Body: io.NopCloser(bytes.NewReader(reply.GetBody()))}, nil
}

// BuildTCPForwardService 构建 TCP 转发服务配置
func BuildTCPForwardService(name string, listenPort int, targets []string, strategy string) *ServiceConfig {
	nodes := make([]*ForwarderNode, 0)
	for i, target := range targets {
		nodes = append(nodes, &ForwarderNode{
			Name: fmt.Sprintf("target-%d", i),
			Addr: target,
		})
	}

	// 默认策略
	if strategy == "" {
		strategy = "round"
	}

	return &ServiceConfig{
		Name: name,
		Addr: fmt.Sprintf(":%d", listenPort),
		Handler: &HandlerConfig{
			Type: "forward",
		},
		Listener: &ListenerConfig{
			Type: "tcp",
		},
		Forwarder: &ForwarderConfig{
			Nodes: nodes,
			Selector: &SelectorConfig{
				Strategy:    strategy,
				MaxFails:    3,
				FailTimeout: 30 * time.Second,
			},
		},
	}
}

// BuildUDPForwardService 构建 UDP 转发服务配置
func BuildUDPForwardService(name string, listenPort int, targets []string, strategy string) *ServiceConfig {
	nodes := make([]*ForwarderNode, 0)
	for i, target := range targets {
		nodes = append(nodes, &ForwarderNode{
			Name: fmt.Sprintf("target-%d", i),
			Addr: target,
		})
	}

	// 默认策略
	if strategy == "" {
		strategy = "round"
	}

	return &ServiceConfig{
		Name: name,
		Addr: fmt.Sprintf(":%d", listenPort),
		Handler: &HandlerConfig{
			Type: "forward",
		},
		Listener: &ListenerConfig{
			Type: "udp",
			Metadata: map[string]any{
				"keepAlive":      true,     // 否保持连接
				"ttl":            "60s",    // UDP 连接超时
				"readBufferSize": 1024 * 4, // UDP读数据缓冲区字节大小
			},
		},
		Forwarder: &ForwarderConfig{
			Nodes: nodes,
			Selector: &SelectorConfig{
				Strategy:    strategy,
				MaxFails:    3,
				FailTimeout: 30 * time.Second,
			},
		},
	}
}

// CreateLimiter 创建限流器 (幂等)
func (c *Client) CreateLimiter(limiter *LimiterConfig) error {
	path := fmt.Sprintf("/config/limiters/%s", limiter.Name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Debugf("限流器 %s 已存在，跳过创建", limiter.Name)
		return nil
	}

	resp, err := c.doRequest("POST", "/config/limiters", limiter)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("创建限流器失败: %s", string(body))
	}

	return nil
}

// DeleteLimiter 删除限流器 (幂等)
func (c *Client) DeleteLimiter(name string) error {
	path := fmt.Sprintf("/config/limiters/%s", name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		logger.Debugf("限流器 %s 不存在，跳过删除", name)
		return nil
	}

	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除限流器失败: %s", string(body))
	}

	return nil
}

// SetObserver 确保节点配置了同一个统计观察器。
func (c *Client) SetObserver(observer *ObserverConfig) error {
	path := fmt.Sprintf("/config/observers/%s", observer.Name)
	current, err := c.resource(path)
	if err != nil {
		return err
	}
	method := http.MethodPost
	if len(current) == 0 {
		path = "/config/observers"
	} else {
		var existing ObserverConfig
		if err = json.Unmarshal(current, &existing); err != nil {
			return err
		}
		if existing.Plugin != nil && observer.Plugin != nil && *existing.Plugin == *observer.Plugin {
			return nil
		}
		method = http.MethodPut
	}

	resp, err := c.doRequest(method, path, observer)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return errors.ErrTunnelObserverCreateFailed
	}

	return nil
}

// DeleteObserver 删除观察器 (幂等)
func (c *Client) DeleteObserver(name string) error {
	path := fmt.Sprintf("/config/observers/%s", name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		logger.Debugf("观察器 %s 不存在，跳过删除", name)
		return nil
	}

	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除观察器失败: %s", string(body))
	}

	return nil
}

// CreateCLimiter 创建并发连接数限制器 (幂等)
func (c *Client) CreateCLimiter(climiter *CLimiterConfig) error {
	path := fmt.Sprintf("/config/climiters/%s", climiter.Name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Debugf("并发连接限制器 %s 已存在，跳过创建", climiter.Name)
		return nil
	}

	resp, err := c.doRequest("POST", "/config/climiters", climiter)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("创建并发连接限制器失败: %s", string(body))
	}

	return nil
}

// DeleteCLimiter 删除并发连接数限制器 (幂等)
func (c *Client) DeleteCLimiter(name string) error {
	path := fmt.Sprintf("/config/climiters/%s", name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		logger.Debugf("并发连接限制器 %s 不存在，跳过删除", name)
		return nil
	}

	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除并发连接限制器失败: %s", string(body))
	}

	return nil
}

// CreateRLimiter 创建请求速率限制器 (幂等)
func (c *Client) CreateRLimiter(rlimiter *RLimiterConfig) error {
	path := fmt.Sprintf("/config/rlimiters/%s", rlimiter.Name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Debugf("请求速率限制器 %s 已存在，跳过创建", rlimiter.Name)
		return nil
	}

	resp, err := c.doRequest("POST", "/config/rlimiters", rlimiter)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("创建请求速率限制器失败: %s", string(body))
	}

	return nil
}

// DeleteRLimiter 删除请求速率限制器 (幂等)
func (c *Client) DeleteRLimiter(name string) error {
	path := fmt.Sprintf("/config/rlimiters/%s", name)
	existing, err := c.resource(path)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		logger.Debugf("请求速率限制器 %s 不存在，跳过删除", name)
		return nil
	}

	resp, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除请求速率限制器失败: %s", string(body))
	}

	return nil
}
