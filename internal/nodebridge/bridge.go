package nodebridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"yagp/internal/buildinfo"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const maxResponseBytes = 4 << 20

type Config struct {
	NodeID           uint64         `yaml:"node_id"`
	Username         string         `yaml:"username"`
	Password         string         `yaml:"password"`
	AdminAddress     string         `yaml:"admin_address"`
	ObserverListen   string         `yaml:"observer_listen"`
	Host             string         `yaml:"host"`
	ReportCAFile     string         `yaml:"report_ca_file"`
	ReportCertSHA256 string         `yaml:"report_cert_sha256,omitempty"`
	Firewall         FirewallConfig `yaml:"firewall,omitempty"`
	NftConfigFile    string         `yaml:"-"`
	ConfigFile       string         `yaml:"-"`
	RuntimeLayout    int            `yaml:"-"`
}

func Run(ctx context.Context, cfg Config) error {
	return run(ctx, cfg, nil)
}

func run(ctx context.Context, cfg Config, dial func(context.Context, string, string) (net.Conn, error)) error {
	if cfg.NodeID == 0 || cfg.Username == "" || cfg.Password == "" || strings.Contains(cfg.Username, ":") {
		return errors.New("node ID and credentials are required (username cannot contain a colon)")
	}
	if !IsLoopbackAddress(cfg.AdminAddress) || !IsLoopbackAddress(cfg.ObserverListen) {
		return errors.New("Gost admin and observer addresses must be loopback IP addresses with ports")
	}
	host, err := nodegrpc.ParsePanelHost(cfg.Host)
	if err != nil {
		return err
	}
	panelClient, err := NewPanelClient(host, cfg.ReportCAFile, cfg.ReportCertSHA256)
	if err != nil {
		return fmt.Errorf("configure panel TLS: %w", err)
	}
	defer panelClient.CloseIdleConnections()
	if dial != nil {
		panelClient.Transport.(*http.Transport).DialContext = dial
	}
	credentials, err := openCredentialManager(cfg, host, panelClient)
	if err != nil {
		return err
	}

	observerListener, err := net.Listen("tcp", cfg.ObserverListen)
	if err != nil {
		return fmt.Errorf("listen for local Gost observer: %w", err)
	}
	defer observerListener.Close()
	if cfg.NftConfigFile != "" {
		restoreCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := restoreNftConfig(restoreCtx, cfg.NodeID, cfg.NftConfigFile)
		cancel()
		if err != nil {
			log.Printf("restore nftables config: %v", err)
		}
	}
	if cfg.ConfigFile != "" {
		restoreCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := restoreTrafficQuota(restoreCtx, cfg)
		cancel()
		if err != nil {
			log.Printf("restore traffic quota: %v", err)
		}
	}

	control := &controlServer{
		config: cfg,
		client: &http.Client{
			Transport: &http.Transport{},
			Timeout:   5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	defer control.client.CloseIdleConnections()
	manager, firewallErr := NewFirewallManager(cfg.NodeID, cfg.Firewall)
	control.firewall = manager
	if firewallErr != nil {
		log.Printf("configure firewall: %v", firewallErr)
	}
	if cfg.ConfigFile != "" {
		restoreCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := control.restoreTransparentRedirect(restoreCtx)
		cancel()
		if err != nil {
			log.Printf("restore transparent redirect: %v", err)
		}
	}
	var current atomic.Pointer[nodegrpc.Session]
	observation := grpc.NewServer()
	defer observation.Stop()
	observer.RegisterObserverServer(observation, &observerServer{
		nodeID:  cfg.NodeID,
		token:   nodegrpc.ObserverToken(cfg.Username, cfg.Password),
		current: &current,
	})
	ready := health.NewServer()
	ready.SetServingStatus(daemonHealthService+buildinfo.Version, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(observation, ready)

	channelCtx, cancelChannel := context.WithCancel(ctx)
	var firewallWorker sync.WaitGroup
	defer func() {
		cancelChannel()
		firewallWorker.Wait()
	}()
	if firewallErr == nil && runtime.GOOS == "linux" && manager.Status().Managed {
		control.firewallChanged = make(chan struct{}, 1)
		firewallWorker.Go(func() { control.maintainFirewall(channelCtx) })
	}
	go maintainCredentials(channelCtx, credentials)
	go maintainPanelChannel(channelCtx, credentials, cfg.ConfigFile, &current, controlDispatcher(control))

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- observation.Serve(observerListener) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErrors:
		return err
	}
}

func IsLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host).IsLoopback()
}

type controlServer struct {
	nodegrpc.UnimplementedV1Server
	config          Config
	client          *http.Client
	firewall        *FirewallManager
	firewallChanged chan struct{}
}

func (s *controlServer) Execute(ctx context.Context, command *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	method, path := command.GetMethod(), command.GetPath()
	if !allowedCommand(method, path) {
		return nil, status.Error(codes.InvalidArgument, "unsupported Gost command")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+s.config.AdminAddress+"/api"+path,
		bytes.NewReader(command.GetBody()))
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot create local Gost request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.config.Username, s.config.Password)
	res, err := s.client.Do(req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "local Gost admin request failed: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, status.Error(codes.Unavailable, "cannot read local Gost response")
	}
	if len(body) > maxResponseBytes {
		return nil, status.Error(codes.ResourceExhausted, "local Gost response is too large")
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && method != http.MethodGet && strings.HasPrefix(path, "/config/services") {
		select {
		case s.firewallChanged <- struct{}{}:
		default:
		}
	}
	return &nodegrpc.CommandReply{StatusCode: int32(res.StatusCode), Body: body}, nil
}

func (s *controlServer) GetObserverEndpoint(context.Context, *emptypb.Empty) (*nodegrpc.ObserverEndpoint, error) {
	return &nodegrpc.ObserverEndpoint{Address: s.config.ObserverListen}, nil
}

var resourceName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
var tunnelChainName = regexp.MustCompile(`^tunnel-[1-9][0-9]*-(clash-)?chain$`)

func allowedCommand(method, path string) bool {
	switch path {
	case "/config":
		return method == http.MethodGet
	case "/config?format=yaml":
		return method == http.MethodPost
	}
	parts := strings.Split(path, "/")
	if (len(parts) != 3 && len(parts) != 4) || parts[0] != "" || parts[1] != "config" {
		return false
	}
	switch parts[2] {
	case "services", "chains", "observers", "limiters", "climiters", "rlimiters", "admissions", "bypasses":
	default:
		return false
	}
	if len(parts) == 3 {
		return method == http.MethodPost
	}
	if !resourceName.MatchString(parts[3]) {
		return false
	}
	if parts[2] == "admissions" || parts[2] == "bypasses" {
		return method == http.MethodGet || method == http.MethodPut
	}
	if method == http.MethodPut {
		switch parts[2] {
		case "observers":
			return true
		case "services":
			return strings.HasPrefix(parts[3], "relay-tunnel-")
		case "chains":
			return tunnelChainName.MatchString(parts[3])
		default:
			return false
		}
	}
	return method == http.MethodGet || method == http.MethodDelete
}

type observerServer struct {
	observer.UnimplementedObserverServer
	nodeID  uint64
	token   string
	current *atomic.Pointer[nodegrpc.Session]
}

func (s *observerServer) Observe(ctx context.Context, req *observer.ObserveRequest) (*observer.ObserveReply, error) {
	if !nodegrpc.MatchObserverToken(ctx, s.token) {
		return nil, status.Error(codes.Unauthenticated, "invalid local observer token")
	}
	session := s.current.Load()
	if session == nil {
		return nil, status.Error(codes.Unavailable, "panel stream is disconnected")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return nodegrpc.NewV1Client(session).Report(ctx, &nodegrpc.ReportRequest{NodeId: s.nodeID, Observation: req})
}
