package nodebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"

	"yagp/pkg/nodegrpc"

	auth "github.com/go-gost/plugin/auth/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type relayAuthServer struct {
	auth.UnimplementedAuthenticatorServer
	configFile string
	token      string
}

func (s *controlServer) validRelayAuther(data []byte) bool {
	var config struct {
		Name   string `json:"name"`
		Plugin struct {
			Type  string `json:"type"`
			Addr  string `json:"addr"`
			Token string `json:"token"`
		} `json:"plugin"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return false
	}
	return nodegrpc.IsRelayAuthService(config.Name) && config.Plugin.Type == "grpc" &&
		IsLoopbackAddress(s.config.ObserverListen) && config.Plugin.Addr == s.config.ObserverListen &&
		config.Plugin.Token == nodegrpc.ObserverToken(s.config.Username, s.config.Password)
}

func (s *relayAuthServer) Authenticate(ctx context.Context, req *auth.AuthenticateRequest) (*auth.AuthenticateReply, error) {
	if !nodegrpc.MatchObserverToken(ctx, s.token) {
		return nil, status.Error(codes.Unauthenticated, "invalid local relay authentication token")
	}
	service := req.GetService()
	if s.configFile == "" || !nodegrpc.IsRelayAuthService(service) {
		return &auth.AuthenticateReply{}, nil
	}
	policy, err := readRelayAuthPolicy(relayAuthPolicyPath(s.configFile, service))
	if errors.Is(err, os.ErrNotExist) {
		return &auth.AuthenticateReply{}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot read local relay authentication policy")
	}
	for _, user := range policy.Users {
		if user.Username == req.GetUsername() && relayUserAuthorized(user, req.GetPassword(), req.GetClient()) {
			return &auth.AuthenticateReply{Ok: true, Id: user.Username}, nil
		}
	}
	return &auth.AuthenticateReply{}, nil
}

func relayUserAuthorized(user nodegrpc.RelayAuthUser, password, client string) bool {
	expected, supplied := sha256.Sum256([]byte(user.Password)), sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(expected[:], supplied[:]) != 1 {
		return false
	}
	if !user.RestrictSources {
		return true
	}

	var address netip.Addr
	if endpoint, err := netip.ParseAddrPort(client); err == nil {
		address = endpoint.Addr()
	} else {
		address, _ = netip.ParseAddr(client)
	}
	return address.IsValid() && address.Zone() == "" && slices.Contains(user.AllowedIPs, address.Unmap().String())
}

func (s *controlServer) relayAuthCommand(command *nodegrpc.CommandRequest, service string) (*nodegrpc.CommandReply, error) {
	if !nodegrpc.IsRelayAuthService(service) {
		return nil, status.Error(codes.InvalidArgument, "invalid relay service name")
	}
	if s.config.ConfigFile == "" {
		return nil, status.Error(codes.Unimplemented, "relay authentication state is unavailable")
	}
	path := relayAuthPolicyPath(s.config.ConfigFile, service)
	reply := &nodegrpc.CommandReply{StatusCode: http.StatusOK}
	switch command.GetMethod() {
	case http.MethodGet:
		_, err := readRelayAuthPolicy(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, status.Error(codes.Internal, "cannot read local relay authentication policy")
		}
		reply.Body, _ = json.Marshal(nodegrpc.RelayAuthStatus{Configured: err == nil})
	case http.MethodPut:
		policy, err := parseRelayAuthPolicy(command.GetBody())
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(policy)
		if err != nil || WriteNodeFile(path, data) != nil {
			return nil, status.Error(codes.Internal, "cannot save local relay authentication policy")
		}
	case http.MethodDelete:
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, status.Error(codes.Internal, "cannot remove local relay authentication policy")
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported relay authentication command")
	}
	return reply, nil
}

func parseRelayAuthPolicy(data []byte) (nodegrpc.RelayAuthConfig, error) {
	var policy nodegrpc.RelayAuthConfig
	if err := json.Unmarshal(data, &policy); err != nil || policy.Users == nil {
		return policy, status.Error(codes.InvalidArgument, "invalid relay authentication policy")
	}
	seen := make(map[string]struct{}, len(policy.Users))
	for i := range policy.Users {
		user := &policy.Users[i]
		if user.Username == "" || user.Password == "" || len(user.Username) > 255 || len(user.Password) > 255 {
			return policy, status.Error(codes.InvalidArgument, "invalid relay credentials")
		}
		if _, ok := seen[user.Username]; ok {
			return policy, status.Error(codes.InvalidArgument, "duplicate relay username")
		}
		seen[user.Username] = struct{}{}
		for j, value := range user.AllowedIPs {
			address, err := netip.ParseAddr(value)
			if err != nil || address.Zone() != "" {
				return policy, status.Error(codes.InvalidArgument, "invalid relay source address")
			}
			user.AllowedIPs[j] = address.Unmap().String()
		}
	}
	return policy, nil
}

func relayAuthPolicyPath(configFile, service string) string {
	return filepath.Join(filepath.Dir(configFile), service+"-auth.json")
}

func readRelayAuthPolicy(path string) (nodegrpc.RelayAuthConfig, error) {
	var policy nodegrpc.RelayAuthConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return policy, err
	}
	err = json.Unmarshal(data, &policy)
	return policy, err
}
