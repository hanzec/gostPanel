package nodebridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ycgp/pkg/nodegrpc"

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
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.NodeID == 0 || cfg.Username == "" || cfg.Password == "" || strings.Contains(cfg.Username, ":") {
		return errors.New("node ID and credentials are required (username cannot contain a colon)")
	}
	if !isLoopbackAddress(cfg.AdminAddress) || !isLoopbackAddress(cfg.ObserverListen) {
		return errors.New("Gost admin and observer addresses must be loopback IP addresses with ports")
	}
	if host, _, err := net.SplitHostPort(cfg.ReportAddress); err != nil || host == "" {
		return errors.New("panel report address must contain a host and port")
	}
	controlTLS, err := credentials.NewServerTLSFromFile(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("load node TLS certificate: %w", err)
	}
	reportTLS, err := nodegrpc.ClientTLS(cfg.ReportCAFile)
	if err != nil {
		return fmt.Errorf("load panel report CA: %w", err)
	}
	reportConn, err := grpc.NewClient(cfg.ReportAddress, grpc.WithTransportCredentials(reportTLS),
		grpc.WithPerRPCCredentials(nodegrpc.PasswordCredentials(cfg.Username, cfg.Password)))
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
	observer.RegisterObserverServer(observation, &observerServer{
		nodeID: cfg.NodeID,
		token:  nodegrpc.ObserverToken(cfg.Username, cfg.Password),
		panel:  nodegrpc.NewPanelClient(reportConn),
	})

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

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && net.ParseIP(host).IsLoopback()
}

type controlServer struct {
	nodegrpc.UnimplementedNodeServer
	config Config
	client *http.Client
}

func (s *controlServer) Execute(ctx context.Context, command *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	if !nodegrpc.MatchCredentials(ctx, s.config.Username, s.config.Password) {
		return nil, status.Error(codes.Unauthenticated, "invalid node credentials")
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
	if !nodegrpc.MatchCredentials(ctx, s.config.Username, s.config.Password) {
		return nil, status.Error(codes.Unauthenticated, "invalid node credentials")
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
	case "services", "chains", "observers", "limiters", "climiters", "rlimiters":
	default:
		return false
	}
	if len(parts) == 3 {
		return method == http.MethodPost
	}
	if !resourceName.MatchString(parts[3]) {
		return false
	}
	return method == http.MethodGet || method == http.MethodDelete || (parts[2] == "observers" && method == http.MethodPut)
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
