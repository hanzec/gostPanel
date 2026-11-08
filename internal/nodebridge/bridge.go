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
	"strings"
	"time"

	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const maxResponseBytes = 4 << 20

type Config struct {
	NodeID         uint64 `yaml:"node_id"`
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	ControlListen  string `yaml:"control_listen"`
	AdminAddress   string `yaml:"admin_address"`
	ObserverListen string `yaml:"observer_listen"`
	ReportAddress  string `yaml:"report_address"`
	ReportCAFile   string `yaml:"report_ca_file"`
	TLSCertFile    string `yaml:"tls_cert_file"`
	TLSKeyFile     string `yaml:"tls_key_file"`
	NftConfigFile  string `yaml:"-"`
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.NodeID == 0 || cfg.Username == "" || cfg.Password == "" || strings.Contains(cfg.Username, ":") {
		return errors.New("node ID and credentials are required (username cannot contain a colon)")
	}
	if !IsLoopbackAddress(cfg.AdminAddress) || !IsLoopbackAddress(cfg.ObserverListen) {
		return errors.New("Gost admin and observer addresses must be loopback IP addresses with ports")
	}
	if host, _, err := net.SplitHostPort(cfg.ReportAddress); err != nil || host == "" {
		return errors.New("panel report address must contain a host and port")
	}
	controlTLS, err := credentials.NewServerTLSFromFile(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("load node TLS certificate: %w", err)
	}
	reportConn, err := connectPanel(cfg)
	if err != nil {
		return fmt.Errorf("create panel report client: %w", err)
	}
	defer reportConn.Close()

	controlListener, err := net.Listen("tcp", cfg.ControlListen)
	if err != nil {
		return fmt.Errorf("listen for node control: %w", err)
	}
	defer controlListener.Close()
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

	control := grpc.NewServer(grpc.Creds(controlTLS))
	defer control.Stop()
	nodegrpc.RegisterNodeServer(control, &controlServer{
		config: cfg,
		client: &http.Client{
			Transport: &http.Transport{},
			Timeout:   5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	})
	observation := grpc.NewServer()
	defer observation.Stop()
	panel := nodegrpc.NewPanelClient(reportConn)
	observer.RegisterObserverServer(observation, &observerServer{
		nodeID: cfg.NodeID,
		token:  nodegrpc.ObserverToken(cfg.Username, cfg.Password),
		panel:  panel,
	})

	ipTicks := time.NewTicker(5 * time.Minute)
	defer ipTicks.Stop()
	reportCtx, cancelReports := context.WithCancel(ctx)
	defer cancelReports()
	go reportPublicIPChanges(reportCtx, cfg.NodeID, panel, DetectPublicIPs, ipTicks.C)

	serveErrors := make(chan error, 2)
	go func() { serveErrors <- control.Serve(controlListener) }()
	go func() { serveErrors <- observation.Serve(observerListener) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErrors:
		return err
	}
}

func connectPanel(cfg Config) (*grpc.ClientConn, error) {
	transport, err := nodegrpc.ClientTLS(cfg.ReportCAFile)
	if err != nil {
		return nil, fmt.Errorf("load panel report CA: %w", err)
	}
	return grpc.NewClient(cfg.ReportAddress, grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(nodegrpc.PasswordCredentials(cfg.Username, cfg.Password)))
}

func IsLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host).IsLoopback()
}

type controlServer struct {
	nodegrpc.UnimplementedNodeServer
	config Config
	client *http.Client
}

func (s *controlServer) authenticate(ctx context.Context) error {
	if !nodegrpc.MatchCredentials(ctx, s.config.Username, s.config.Password) {
		return status.Error(codes.Unauthenticated, "invalid node credentials")
	}
	return nil
}

func (s *controlServer) Execute(ctx context.Context, command *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if err := s.authenticate(ctx); err != nil {
		return nil, err
	}
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
	return &nodegrpc.CommandReply{StatusCode: int32(res.StatusCode), Body: body}, nil
}

func (s *controlServer) GetObserverEndpoint(ctx context.Context, _ *emptypb.Empty) (*nodegrpc.ObserverEndpoint, error) {
	if err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	return &nodegrpc.ObserverEndpoint{Address: s.config.ObserverListen}, nil
}

var resourceName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

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
		return parts[2] == "observers" || (parts[2] == "services" && strings.HasPrefix(parts[3], "relay-tunnel-"))
	}
	return method == http.MethodGet || method == http.MethodDelete
}

type observerServer struct {
	observer.UnimplementedObserverServer
	nodeID uint64
	token  string
	panel  nodegrpc.PanelClient
}

func (s *observerServer) Observe(ctx context.Context, req *observer.ObserveRequest) (*observer.ObserveReply, error) {
	if !nodegrpc.MatchObserverToken(ctx, s.token) {
		return nil, status.Error(codes.Unauthenticated, "invalid local observer token")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.panel.Report(ctx, &nodegrpc.ReportRequest{NodeId: s.nodeID, Observation: req})
}
