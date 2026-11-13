package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/utils"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"
)

func TestSharedFixedIngressThroughGost(t *testing.T) {
	binary := os.Getenv("YAGP_TEST_GOST_BINARY")
	if binary == "" {
		t.Skip("set YAGP_TEST_GOST_BINARY to a Gost binary")
	}
	for _, protocol := range []model.RuleProtocol{model.RuleProtocolTCP, model.RuleProtocolUDP} {
		t.Run(string(protocol), func(t *testing.T) {
			db := newServiceTestDB(t)
			loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
			listeners := reserveSharedGostListeners(t, string(protocol))
			admin, relayA, relayB, ingress := listeners[0], listeners[1], listeners[2], listeners[3]
			targetA := startSharedGostTarget(t, string(protocol), "first")
			targetB := startSharedGostTarget(t, string(protocol), "second")
			exitA := &model.GostRule{Role: model.EndpointRoleExit, Targets: []string{targetA}}
			exitB := &model.GostRule{Role: model.EndpointRoleExit, Targets: []string{targetB}}
			api := startSharedGostFixture(t, binary, admin, relayA, relayB)
			entryNode := startServiceTestNode(t, api)
			saveServiceFixtures(t, db, &entryNode)
			endpoint := &model.GostRule{Name: "shared entry", NodeID: &entryNode.ID, Role: model.EndpointRoleEntry, Protocol: protocol, ListenIP: "127.0.0.1"}
			_, port, err := net.SplitHostPort(ingress)
			requireServiceError(t, err, nil)
			endpoint.ListenPort, err = strconv.Atoi(port)
			requireServiceError(t, err, nil)
			saveServiceFixtures(t, db, endpoint)
			tunnels := []*model.GostTunnel{
				{ID: 1, EntryRule: endpoint, ExitRule: exitA, ChainID: tunnelChainName(1)},
				{ID: 2, EntryRule: endpoint, ExitRule: exitB, ChainID: tunnelChainName(2)},
			}
			client := utils.GetGostClient(&entryNode)
			// Start in the same configuration as an existing single-tunnel endpoint.
			single := buildFixedIngressService(endpoint, entranceServiceName(endpoint.ID), endpoint.ListenIP, endpoint.ListenPort, exitA.Targets, "round")
			single.Handler.Chain = tunnels[0].ChainID
			_, err = client.EnsureService(single)
			requireServiceError(t, err, nil)
			requireServiceError(t, NewTunnelService(db).applySharedFixedIngress(client, endpoint, tunnels), nil)
			observed := map[string]bool{}
			for range 4 {
				observed[exchangeSharedGostMessage(t, string(protocol), ingress)] = true
			}
			if len(observed) != 2 || !observed["first"] || !observed["second"] {
				t.Fatalf("shared ingress reached %v, want both targets", observed)
			}

			requireServiceError(t, NewTunnelService(db).applySharedFixedIngress(client, endpoint, tunnels[1:]), nil)
			for range 2 {
				if got := exchangeSharedGostMessage(t, string(protocol), ingress); got != "second" {
					t.Fatalf("shared ingress after stopping first route received %q", got)
				}
			}
		})
	}
}

func reserveSharedGostListeners(t *testing.T, protocol string) []string {
	t.Helper()
	var addresses []string
	for _, network := range []string{"tcp", "tcp", "tcp", protocol} {
		if network == "udp" {
			socket, err := net.ListenPacket(network, "127.0.0.1:0")
			requireServiceError(t, err, nil)
			defer socket.Close()
			addresses = append(addresses, socket.LocalAddr().String())
			continue
		}
		listener, err := net.Listen(network, "127.0.0.1:0")
		requireServiceError(t, err, nil)
		defer listener.Close()
		addresses = append(addresses, listener.Addr().String())
	}
	return addresses
}

func startSharedGostTarget(t *testing.T, protocol, reply string) string {
	t.Helper()
	if protocol == "udp" {
		socket, err := net.ListenPacket(protocol, "127.0.0.1:0")
		requireServiceError(t, err, nil)
		t.Cleanup(func() { _ = socket.Close() })
		go func() {
			for {
				var payload [32]byte
				_, peer, err := socket.ReadFrom(payload[:])
				if err != nil {
					return
				}
				_, _ = socket.WriteTo([]byte(reply), peer)
			}
		}()
		return socket.LocalAddr().String()
	}
	listener, err := net.Listen(protocol, "127.0.0.1:0")
	requireServiceError(t, err, nil)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				var payload [32]byte
				if _, err := conn.Read(payload[:]); err == nil {
					_, _ = io.WriteString(conn, reply)
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func exchangeSharedGostMessage(t *testing.T, protocol, address string) string {
	t.Helper()
	conn, err := net.DialTimeout(protocol, address, time.Second)
	requireServiceError(t, err, nil)
	defer conn.Close()
	requireServiceError(t, conn.SetDeadline(time.Now().Add(3*time.Second)), nil)
	_, err = io.WriteString(conn, "request")
	requireServiceError(t, err, nil)
	var response [32]byte
	count, err := conn.Read(response[:])
	requireServiceError(t, err, nil)
	return string(response[:count])
}

func startSharedGostFixture(t *testing.T, binary, address string, relayAddresses ...string) *sharedGostTestNode {
	t.Helper()
	var chains []*gost.ChainConfig
	var relays []*gost.ServiceConfig
	for i, endpoint := range relayAddresses {
		id := uint(i + 1)
		name := relayServiceName(id)
		credentials := &gost.AuthConfig{Username: name, Password: "fixture"}
		chains = append(chains, newTunnelChain(tunnelChainName(id), endpoint, "tcp", nil, credentials))
		relays = append(relays, &gost.ServiceConfig{Name: name, Addr: endpoint, Listener: gost.RelayListener("tcp", nil),
			Handler: &gost.HandlerConfig{Type: "relay", Auth: credentials}})
	}
	config, err := json.Marshal(map[string]any{"services": relays, "chains": chains,
		"api": map[string]string{"addr": address, "pathPrefix": "/api"}})
	requireServiceError(t, err, nil)
	path := filepath.Join(t.TempDir(), "gost.json")
	requireServiceError(t, os.WriteFile(path, config, 0600), nil)
	cmd := exec.Command(binary, "-C", path)
	logFile, err := os.Create(filepath.Join(t.TempDir(), "gost.log"))
	requireServiceError(t, err, nil)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	requireServiceError(t, cmd.Start(), nil)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
		_ = logFile.Close()
		if t.Failed() {
			log, _ := os.ReadFile(logFile.Name())
			t.Log(string(log))
		}
	})
	node := &sharedGostTestNode{lifecycleNode: lifecycleNode{t: t}, baseURL: "http://" + address + "/api", http: &http.Client{Timeout: time.Second}}
	t.Cleanup(node.http.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		reply, err := node.Execute(ctx, &nodegrpc.CommandRequest{Method: http.MethodGet, Path: "/config"})
		if err == nil && reply.GetStatusCode() == http.StatusOK {
			return node
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Gost did not start: %v", err)
		case <-done:
			t.Fatal("Gost exited during startup")
		case <-tick.C:
		}
	}
}

type sharedGostTestNode struct {
	lifecycleNode
	baseURL string
	http    *http.Client
}

func (n *sharedGostTestNode) Execute(ctx context.Context, cmd *nodegrpc.CommandRequest) (*nodegrpc.CommandReply, error) {
	req, err := http.NewRequestWithContext(ctx, cmd.GetMethod(), n.baseURL+cmd.GetPath(), bytes.NewReader(cmd.GetBody()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return &nodegrpc.CommandReply{StatusCode: int32(resp.StatusCode), Body: data}, err
}
