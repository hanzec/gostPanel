package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
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

const nodeStateDir = "/etc/yagp-node"

func enroll(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ExitOnError)
	address := flags.String("panel-address", "", "panel registration address HOST:PORT")
	output := flags.String("output", "", "staging directory for node files")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "enroll reads the registration token from stdin")
		flags.PrintDefaults()
	}
	_ = flags.Parse(args)
	if *output == "" || flags.NArg() != 0 {
		return errors.New("enroll requires --panel-address and --output")
	}
	token, err := io.ReadAll(io.LimitReader(os.Stdin, 128))
	if err != nil {
		return fmt.Errorf("read registration token: %w", err)
	}
	nodeID, err := registerNode(ctx, *address, strings.TrimSpace(string(token)), nodeStateDir, *output)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, nodeID)
	return err
}

func registerNode(ctx context.Context, address, token, stateDir, output string) (uint64, error) {
	host, port, err := net.SplitHostPort(address)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || strings.ContainsAny(host, "/ \\?#\t\r\n") || portErr != nil || portNumber == 0 {
		return 0, errors.New("panel address must be HOST:PORT")
	}
	// The token ID is a local preflight hint. The panel verifies the full token.
	expectedID, err := nodegrpc.ParseRegistrationTokenID(token)
	if err != nil {
		return 0, err
	}
	basePath := filepath.Join(stateDir, strconv.FormatUint(expectedID, 10))
	if _, err = os.Lstat(basePath); err == nil {
		return 0, fmt.Errorf("existing node directory %s, move it before registering a replacement", basePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	caFile, err := panelCAFile(stateDir)
	if err != nil {
		return 0, err
	}
	transport, err := nodegrpc.ClientTLS(caFile)
	if err != nil {
		return 0, fmt.Errorf("load panel CA: %w", err)
	}
	cert, key, err := newNodeCertificate()
	if err != nil {
		return 0, fmt.Errorf("create node TLS key pair: %w", err)
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	ipv4, ipv6 := detectPublicIPs(ctx)
	rpcCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reply, err := nodegrpc.NewPanelClient(conn).RegisterNode(rpcCtx, &nodegrpc.RegisterNodeRequest{
		Token: token, NodeCertificatePem: string(cert),
		Ipv4: ipv4, Ipv6: ipv6,
	})
	if err != nil {
		return 0, fmt.Errorf("register with panel: %w", err)
	}
	if reply.GetNodeId() != expectedID || reply.GetUsername() == "" || strings.Contains(reply.GetUsername(), ":") ||
		reply.GetPassword() == "" || reply.GetControlPort() == 0 || reply.GetControlPort() > 65535 {
		return 0, errors.New("panel returned invalid node enrollment data")
	}
	config := nodebridge.Config{
		NodeID: reply.GetNodeId(), Username: reply.GetUsername(), Password: reply.GetPassword(),
		ControlListen: ":" + strconv.FormatUint(uint64(reply.GetControlPort()), 10),
		ReportAddress: address, ReportCAFile: caFile,
		TLSCertFile: filepath.Join(basePath, "node.crt"), TLSKeyFile: filepath.Join(basePath, "node.key"),
	}
	if err = writeEnrollment(output, config, cert, key); err != nil {
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

func newNodeCertificate() ([]byte, []byte, error) {
	publicKey, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	certificate := &x509.Certificate{
		SerialNumber: serial.Add(serial, big.NewInt(1)),
		Subject:      pkix.Name{CommonName: nodegrpc.NodeCertificateName}, DNSNames: []string{nodegrpc.NodeCertificateName},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, certificate, certificate, publicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func writeEnrollment(output string, config nodebridge.Config, cert, key []byte) error {
	// Keep all three ports bound until configuration has been written so their assignments are distinct.
	control, err := net.Listen("tcp", config.ControlListen)
	if err != nil {
		return fmt.Errorf("listen on node control port: %w", err)
	}
	defer control.Close()
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
	for name, data := range map[string][]byte{"node.yaml": nodeYAML, "gost.yaml": gostYAML, "node.crt": cert, "node.key": key} {
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
