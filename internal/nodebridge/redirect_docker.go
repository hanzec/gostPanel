package nodebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

type dockerBridge struct {
	Name    string
	Address netip.Addr
}

func discoverDockerBridges(ctx context.Context) ([]dockerBridge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, nil
	}
	reported := localDockerBridgeNames(ctx, "/var/run/docker.sock")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := dockerBridgeNames("/sys/class/net", reported)
	if err != nil {
		return nil, err
	}
	var bridges []dockerBridge
	for _, name := range names {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return nil, fmt.Errorf("find Docker bridge %s: %w", name, err)
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("read Docker bridge %s addresses: %w", name, err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr().Is4() && prefix.Addr().IsGlobalUnicast() {
				bridges = append(bridges, dockerBridge{Name: name, Address: prefix.Addr()})
				break
			}
		}
	}
	return bridges, nil
}

func localDockerBridgeNames(ctx context.Context, socket string) map[string]struct{} {
	client := http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// Use the local socket regardless of CLI settings or DOCKER_HOST.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/networks", nil)
	if err != nil {
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var networks []struct {
		ID      string
		Name    string
		Driver  string
		Options map[string]string
	}
	if err := json.NewDecoder(response.Body).Decode(&networks); err != nil {
		return nil
	}
	names := make(map[string]struct{})
	for _, network := range networks {
		if network.Driver != "bridge" {
			continue
		}
		name := network.Options["com.docker.network.bridge.name"]
		if name == "" && network.Name == "bridge" {
			name = "docker0"
		}
		if name == "" && len(network.ID) >= 12 {
			candidate := "br-" + network.ID[:12]
			if conventionalDockerBridge(candidate) {
				name = candidate
			}
		}
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func dockerBridgeNames(sysfsPath string, reported map[string]struct{}) ([]string, error) {
	devices, err := os.ReadDir(sysfsPath)
	if err != nil {
		return nil, fmt.Errorf("list Linux network interfaces: %w", err)
	}
	var bridges []string
	for _, device := range devices {
		name := device.Name()
		_, reportedByDocker := reported[name]
		if (reported != nil && !reportedByDocker) || (reported == nil && !conventionalDockerBridge(name)) ||
			strings.ContainsAny(name, "*\"\\\r\n\t") {
			continue
		}
		_, err := os.Stat(filepath.Join(sysfsPath, name, "bridge"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect network interface %s: %w", name, err)
		}
		bridges = append(bridges, name)
	}
	if index := slices.Index(bridges, "docker0"); index > 0 {
		bridges = append([]string{"docker0"}, slices.Delete(bridges, index, index+1)...)
	}
	return bridges, nil
}

func conventionalDockerBridge(name string) bool {
	if name == "docker0" {
		return true
	}
	id, ok := strings.CutPrefix(name, "br-")
	if !ok || len(id) != 12 || id != strings.ToLower(id) {
		return false
	}
	_, err := strconv.ParseUint(id, 16, 48)
	return err == nil
}
