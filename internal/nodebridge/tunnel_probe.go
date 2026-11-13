package nodebridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	tunnelProbeHost    = "127.0.0.1"
	tunnelProbeTTL     = 15 * time.Second
	tunnelProbeTimeout = 8 * time.Second
)

func (s *controlServer) StartTunnelProbe(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.TunnelProbeEndpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP(tunnelProbeHost)})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "出口节点无法启动测试监听")
	}
	token := make([]byte, sha256.Size)
	rand.Read(token)
	go serveTunnelProbe(listener, token, time.Now().Add(tunnelProbeTTL))
	return &nodegrpc.TunnelProbeEndpoint{Address: listener.Addr().String(), Token: token}, nil
}

func serveTunnelProbe(listener *net.TCPListener, token []byte, deadline time.Time) {
	defer listener.Close()
	// The test spans two RPCs. The exit listener expires independently of the first RPC.
	_ = listener.SetDeadline(deadline)
	conn, err := listener.AcceptTCP()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	var request [sha256.Size]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil || !bytes.Equal(request[:], token) {
		return
	}
	// A plain echo from a different service cannot pass the response check.
	response := sha256.Sum256(request[:])
	_, _ = conn.Write(response[:])
}

func (s *controlServer) ProbeTunnel(ctx context.Context, req *nodegrpc.TunnelProbeRequest) (*nodegrpc.TunnelProbeReply, error) {
	name, address, token := req.GetChainName(), req.GetAddress(), req.GetToken()
	target, err := netip.ParseAddrPort(address)
	if err != nil || target.Addr() != netip.MustParseAddr(tunnelProbeHost) || target.Port() == 0 || len(token) != sha256.Size ||
		!tunnelChainName.MatchString(name) || strings.HasSuffix(name, "-clash-chain") || strings.HasSuffix(name, "-shared-chain") {
		return nil, status.Error(codes.InvalidArgument, "无效的隧道测试请求")
	}
	probeCtx, cancel := context.WithTimeout(ctx, tunnelProbeTimeout)
	defer cancel()
	relay, chain, err := s.tunnelProbeChain(probeCtx, name)
	if err != nil {
		return nil, err
	}
	if s.config.ConfigFile == "" {
		return nil, tunnelProbeError(probeCtx, nil, codes.Internal, "entry_gost_process", "找不到节点安装目录")
	}
	logs := tunnelProbeLog{recognized: make(chan struct{})}
	elapsed, err := runTunnelProbe(probeCtx, s.config.ConfigFile, name, address, token, chain, &logs)
	if err != nil {
		return nil, diagnoseTunnelProbeFailure(ctx, err, relay, logs.failure)
	}
	return &nodegrpc.TunnelProbeReply{RoundTripMs: elapsed.Milliseconds()}, nil
}

func (s *controlServer) tunnelProbeChain(ctx context.Context, name string) (*gost.NodeConfig, json.RawMessage, error) {
	reply, err := s.Execute(ctx, &nodegrpc.CommandRequest{Method: http.MethodGet, Path: "/config/chains/" + name})
	if err != nil {
		return nil, nil, tunnelProbeError(ctx, err, codes.Internal, "entry_gost_api", "无法连接入口节点的 Gost 管理接口")
	}
	switch reply.GetStatusCode() {
	case http.StatusNotFound:
		return nil, nil, tunnelProbeError(ctx, nil, codes.NotFound, "entry_chain", "入口节点没有此隧道的 Gost 转发链")
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, nil, tunnelProbeStatus(codes.Internal, "入口节点的 Gost 管理接口拒绝认证", "entry_gost_api",
			map[string]string{"gost_error": "admin_authentication"})
	case http.StatusOK:
	default:
		return nil, nil, tunnelProbeError(ctx, nil, codes.Internal, "entry_gost_api", "无法读取入口节点的 Gost 转发链")
	}
	var response gost.GostResponse
	if err := json.Unmarshal(reply.GetBody(), &response); err != nil {
		return nil, nil, tunnelProbeError(ctx, nil, codes.Internal, "entry_gost_api", "无法解析入口节点的 Gost 响应")
	}
	if len(response.Data) == 0 || string(response.Data) == "null" {
		return nil, nil, tunnelProbeError(ctx, nil, codes.NotFound, "entry_chain", "入口节点没有此隧道的 Gost 转发链")
	}
	var chain gost.ChainConfig
	if err := json.Unmarshal(response.Data, &chain); err == nil {
		if relay := tunnelProbeRelay(chain, name); relay != nil {
			// Keep transport options and credentials exactly as returned by Gost.
			return relay, response.Data, nil
		}
	}
	return nil, nil, tunnelProbeError(ctx, nil, codes.FailedPrecondition, "entry_chain", "入口节点的 Gost 转发链配置无效")
}

func tunnelProbeRelay(chain gost.ChainConfig, name string) *gost.NodeConfig {
	if chain.Name != name || len(chain.Hops) != 1 || chain.Hops[0] == nil || len(chain.Hops[0].Nodes) != 1 {
		return nil
	}
	relayAddress, ok := chain.TunnelRelayAddress()
	host, port, err := net.SplitHostPort(relayAddress)
	node := chain.Hops[0].Nodes[0]
	if !ok || err != nil || host == "" || port == "" || node.Connector == nil || node.Connector.Type != "relay" ||
		node.Dialer == nil || node.Dialer.Type == "" {
		return nil
	}
	return node
}

func runTunnelProbe(ctx context.Context, configFile, chainName, target string, token []byte, chain json.RawMessage, logs *tunnelProbeLog) (time.Duration, error) {
	const stage = "entry_gost_process"
	installDir, err := filepath.Abs(filepath.Dir(configFile))
	if err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Internal, stage, "找不到节点安装目录")
	}
	temp, err := os.MkdirTemp("", "yagp-probe-")
	if err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Internal, stage, "入口节点无法创建测试目录")
	}
	defer os.RemoveAll(temp)
	socket := filepath.Join(temp, "probe.sock")
	configPath := filepath.Join(temp, "probe.json")
	config, err := tunnelProbeConfig(chain, chainName, socket, target)
	if err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Internal, stage, "无法生成隧道测试配置")
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Internal, stage, "入口节点无法写入测试配置")
	}

	// Use the installed Gost with a private configuration so live saves cannot persist the test service.
	cmd := exec.CommandContext(ctx, filepath.Join(installDir, "gost"), "-C", configPath)
	cmd.Dir = installDir
	cmd.Stdout, cmd.Stderr = logs, logs
	for _, value := range cmd.Environ() {
		if !strings.HasPrefix(value, "GOST_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	if err := cmd.Start(); err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			logs.failure = "binary_missing"
		case errors.Is(err, os.ErrPermission):
			logs.failure = "binary_permission"
		}
		return 0, tunnelProbeError(ctx, err, codes.Internal, stage, "入口节点无法启动 Gost 测试进程")
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()
	conn, err := dialTunnelProbe(ctx, socket, exited)
	if err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Internal, stage, "入口节点的 Gost 测试进程未能就绪")
	}
	_ = os.Remove(configPath)
	elapsed, err := exchangeTunnelProbe(ctx, conn, token)
	_ = conn.Close()
	if err != nil {
		// Gost can log the Relay status just after the Unix socket reaches EOF.
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-logs.recognized:
		case <-exited:
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	return elapsed, err
}

func tunnelProbeConfig(chain json.RawMessage, chainName, socket, target string) ([]byte, error) {
	service := gost.BuildTCPForwardService("yagp-tunnel-probe", 0, []string{target}, "")
	service.Addr = socket
	service.Listener.Type = "unix"
	service.Handler.Chain = chainName
	service.Forwarder.Nodes[0].Network = "tcp"
	return json.Marshal(struct {
		Services []*gost.ServiceConfig `json:"services"`
		Chains   []json.RawMessage     `json:"chains"`
		Log      map[string]string     `json:"log"`
	}{
		Services: []*gost.ServiceConfig{service},
		Chains:   []json.RawMessage{chain},
		// Some Relay rejections are logged at debug level by Gost.
		Log: map[string]string{"level": "debug", "format": "json"},
	})
}

func dialTunnelProbe(ctx context.Context, socket string, exited <-chan struct{}) (net.Conn, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var dialer net.Dialer
	for {
		conn, err := dialer.DialContext(ctx, "unix", socket)
		if err == nil {
			return conn, nil
		}
		select {
		case <-exited:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func exchangeTunnelProbe(ctx context.Context, conn net.Conn, token []byte) (time.Duration, error) {
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	start := time.Now()
	if _, err := conn.Write(token); err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Unavailable, "relay_write", "无法通过隧道发送测试数据")
	}
	var response [sha256.Size]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return 0, tunnelProbeError(ctx, err, codes.Unavailable, "relay_read", "隧道未返回完整的测试数据")
	}
	if response != sha256.Sum256(token) {
		return 0, tunnelProbeError(ctx, nil, codes.DataLoss, "relay_response", "隧道返回的测试数据校验失败")
	}
	return time.Since(start), nil
}

func tunnelProbeError(ctx context.Context, err error, code codes.Code, stage, message string) error {
	switch {
	case ctx.Err() != nil:
		st := status.FromContextError(ctx.Err())
		code, message = st.Code(), st.Message()
	case errors.Is(err, os.ErrDeadlineExceeded):
		code, message = codes.DeadlineExceeded, "隧道测试超时"
	}
	return tunnelProbeStatus(code, message, stage, nil)
}

func tunnelProbeStatus(code codes.Code, message, stage string, evidence map[string]string) error {
	st := status.New(code, message)
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Domain: "yagp.tunnel_probe", Reason: stage, Metadata: evidence,
	})
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}
