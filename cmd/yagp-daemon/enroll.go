package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yagp/internal/nodebridge"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
)

const nodeStateDir = "/etc/yagp-daemon"

func enroll(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ExitOnError)
	host := flags.String("host", "", "panel DNS name or IP address")
	output := flags.String("output", "", "staging directory for node files")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "enroll reads the registration token from stdin")
		flags.PrintDefaults()
	}
	_ = flags.Parse(args)
	if *host == "" || *output == "" || flags.NArg() != 0 {
		return errors.New("enroll requires --host and --output")
	}
	token, err := io.ReadAll(io.LimitReader(os.Stdin, 128))
	if err != nil {
		return fmt.Errorf("read registration token: %w", err)
	}
	nodeID, err := registerNode(ctx, *host, strings.TrimSpace(string(token)), nodeStateDir, *output, nodebridge.DetectPublicIPs)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, nodeID)
	return err
}

func registerNode(ctx context.Context, host, token, stateDir, output string, detectIPs func(context.Context) (string, string), dialOptions ...grpc.DialOption) (uint64, error) {
	panelHost, err := nodegrpc.ParsePanelHost(host)
	if err != nil {
		return 0, err
	}
	// The token ID is a local preflight hint. The panel verifies the full token.
	expectedID, fingerprint, err := nodegrpc.ParseRegistrationToken(token)
	if err != nil {
		return 0, err
	}
	basePath := filepath.Join(stateDir, strconv.FormatUint(expectedID, 10))
	if _, err = os.Lstat(basePath); err == nil {
		return 0, fmt.Errorf("existing node directory %s, move it before registering a replacement", basePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	var caFile string
	if fingerprint == "" {
		caFile, err = panelCAFile(stateDir)
		if err != nil {
			return 0, err
		}
	}
	transport, err := panelHost.ClientTLS(caFile, fingerprint)
	if err != nil {
		return 0, fmt.Errorf("load panel CA: %w", err)
	}
	conn, err := panelHost.NewClientConn(append(dialOptions, grpc.WithTransportCredentials(transport))...)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	ipv4, ipv6 := detectIPs(ctx)
	rpcCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reply, err := nodegrpc.NewV1Client(conn).RegisterNode(rpcCtx, &nodegrpc.RegisterNodeRequest{
		Token: token,
		Ipv4:  ipv4, Ipv6: ipv6,
	})
	if err != nil {
		return 0, fmt.Errorf("register with panel: %w", err)
	}
	if reply.GetNodeId() != expectedID || reply.GetUsername() == "" || strings.Contains(reply.GetUsername(), ":") ||
		reply.GetPassword() == "" {
		return 0, errors.New("panel returned invalid node enrollment data")
	}
	config := nodebridge.Config{
		NodeID: reply.GetNodeId(), Username: reply.GetUsername(), Password: reply.GetPassword(),
		Host: panelHost.Host(), ReportCAFile: caFile, ReportCertSHA256: fingerprint,
	}
	if err = writeEnrollment(output, config); err != nil {
		return 0, fmt.Errorf("registration succeeded, but local configuration failed: %w", err)
	}
	return config.NodeID, nil
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
