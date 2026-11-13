package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yagp/internal/nodebridge"
	"yagp/pkg/nodegrpc"

	"gopkg.in/yaml.v3"
)

func enroll(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ExitOnError)
	host := flags.String("host", "", "panel DNS name or IP address")
	output := flags.String("output", "", "staging directory for node files")
	existing := flags.String("existing", "", "configuration file of a node to re-enroll")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "enroll reads the registration token from stdin")
		flags.PrintDefaults()
	}
	_ = flags.Parse(args)
	if *host == "" || (*output == "") == (*existing == "") || flags.NArg() != 0 {
		return errors.New("enroll requires --host and exactly one of --output or --existing")
	}
	token, err := io.ReadAll(io.LimitReader(os.Stdin, 128))
	if err != nil {
		return fmt.Errorf("read registration token: %w", err)
	}
	nodeID, err := registerNode(ctx, *host, strings.TrimSpace(string(token)), nodebridge.DefaultNodeStateDir, *output, *existing, nodebridge.DetectPublicIPs)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, nodeID)
	return err
}

func registerNode(ctx context.Context, host, token, stateDir, output, existing string, detectIPs func(context.Context) (string, string), dialOptions ...func(context.Context, string, string) (net.Conn, error)) (uint64, error) {
	panelHost, err := nodegrpc.ParsePanelHost(host)
	if err != nil {
		return 0, err
	}
	// The token ID is a local preflight hint. The panel verifies the full token.
	expectedID, fingerprint, err := nodegrpc.ParseRegistrationToken(token)
	if err != nil {
		return 0, err
	}
	config, err := enrollmentConfig(stateDir, existing, expectedID)
	if err != nil {
		return 0, err
	}
	var caFile string
	if fingerprint == "" {
		caFile, err = panelCAFile(stateDir)
		if err != nil {
			return 0, err
		}
		if caFile == "" && config.Host == panelHost.Host() {
			caFile = config.ReportCAFile
		}
	}
	client, err := nodebridge.NewPanelClient(panelHost, caFile, fingerprint)
	if err != nil {
		return 0, fmt.Errorf("load panel CA: %w", err)
	}
	if len(dialOptions) > 0 {
		client.Transport.(*http.Transport).DialContext = dialOptions[0]
	}
	defer client.CloseIdleConnections()
	ipv4, ipv6 := detectIPs(ctx)
	rpcCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reply := new(nodegrpc.RegisterNodeReply)
	request := &nodegrpc.RegisterNodeRequest{
		Token: token,
		Ipv4:  ipv4, Ipv6: ipv6,
	}
	if err = nodegrpc.PostProtobuf(rpcCtx, client, panelHost.HTTPURL(nodegrpc.RegisterPath), request, reply); err != nil {
		var response *nodegrpc.HTTPStatusError
		if ipv4 == "" && ipv6 == "" && errors.As(err, &response) && response.StatusCode == http.StatusBadRequest {
			return 0, fmt.Errorf("register with panel: could not detect a node IP address: %w", err)
		}
		return 0, fmt.Errorf("register with panel: %w", err)
	}
	if reply.GetNodeId() != expectedID || reply.GetUsername() == "" || strings.Contains(reply.GetUsername(), ":") ||
		reply.GetPassword() == "" {
		return 0, errors.New("panel returned invalid node enrollment data")
	}
	if existing != "" && (config.Username != reply.GetUsername() || config.Password != reply.GetPassword()) {
		return 0, errors.New("panel local Gost credentials do not match the existing node")
	}
	config.Username, config.Password = reply.GetUsername(), reply.GetPassword()
	config.Host, config.ReportCAFile, config.ReportCertSHA256 = panelHost.Host(), caFile, fingerprint
	if err = saveEnrollment(output, existing, config, reply.GetCredentials()); err != nil {
		return 0, fmt.Errorf("registration succeeded, but local configuration failed: %w", err)
	}
	return config.NodeID, nil
}

func enrollmentConfig(stateDir, existing string, id uint64) (nodebridge.Config, error) {
	if existing != "" {
		cfg, err := readConfig(existing)
		if err != nil {
			return nodebridge.Config{}, err
		}
		if cfg.NodeID != id {
			return nodebridge.Config{}, errors.New("registration token is for a different node")
		}
		return cfg, nil
	}
	for _, path := range []string{filepath.Join(stateDir, "node.yaml"), filepath.Join(stateDir, strconv.FormatUint(id, 10))} {
		if _, err := os.Lstat(path); err == nil {
			return nodebridge.Config{}, fmt.Errorf("existing node files at %s; use --existing to re-enroll", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nodebridge.Config{}, err
		}
	}
	return nodebridge.Config{NodeID: id}, nil
}

func saveEnrollment(output, existing string, config nodebridge.Config, credentials *nodegrpc.NodeCredentials) error {
	encoded, err := nodebridge.EncodePanelCredentials(credentials)
	if err != nil {
		return err
	}
	if existing == "" {
		if err := writeEnrollment(output, config); err != nil {
			return err
		}
		return writeNewFile(filepath.Join(output, nodebridge.PanelCredentialsFile), encoded, 0600)
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err = nodebridge.WriteNodeFile(existing, data); err != nil {
		return err
	}
	return nodebridge.WriteNodeFile(filepath.Join(filepath.Dir(existing), nodebridge.PanelCredentialsFile), encoded)
}

func panelCAFile(stateDir string) (string, error) {
	if path := os.Getenv("YAGP_PANEL_CA_FILE"); path != "" {
		return filepath.Abs(path)
	}
	path := filepath.Join(stateDir, "panel-ca.pem")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "", nil
}

func writeEnrollment(output string, config nodebridge.Config) error {
	// Hold the local listeners until both distinct port assignments have been written.
	admin, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("select local Gost API port: %w", err)
	}
	defer admin.Close()
	observer, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("select local observer port: %w", err)
	}
	defer observer.Close()
	config.AdminAddress, config.ObserverListen = admin.Addr().String(), observer.Addr().String()
	nodeYAML, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	gostYAML, err := yaml.Marshal(map[string]any{"api": map[string]any{
		"addr": config.AdminAddress, "pathPrefix": "/api",
		"auth": map[string]string{"username": config.Username, "password": config.Password},
	}})
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"node.yaml": nodeYAML, "gost.yaml": gostYAML} {
		if err = writeNewFile(filepath.Join(output, name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	return errors.Join(err, file.Close())
}
