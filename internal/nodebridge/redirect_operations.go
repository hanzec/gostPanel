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
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"time"

	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Bump this when the generated rules change. Revisions also track node-specific changes.
const redirectRulesVersion uint32 = 1

var redirectOwnerName = regexp.MustCompile(`^rule-[1-9][0-9]{0,19}$`)

type redirectState struct {
	Version       uint32       `json:"version"`
	Revision      uint64       `json:"revision"`
	Owner         string       `json:"owner,omitempty"`
	Port          uint32       `json:"port,omitempty"`
	Mark          uint32       `json:"mark,omitempty"`
	RelayIPs      []netip.Addr `json:"relay_ips,omitempty"`
	DockerAddress netip.Addr   `json:"docker_address,omitempty"`
	Bridges       []string     `json:"bridges,omitempty"`
}

func (s *controlServer) ConfigureTransparentRedirect(ctx context.Context, req *nodegrpc.TransparentRedirectRequest) (*nodegrpc.TransparentRedirectReply, error) {
	if runtime.GOOS != "linux" || s.config.ConfigFile == "" {
		return nil, status.Error(codes.Unimplemented, "transparent redirect requires a Linux node with a configuration file")
	}
	if !redirectOwnerName.MatchString(req.GetOwner()) {
		return nil, status.Error(codes.InvalidArgument, "invalid transparent redirect owner")
	}

	const prepare = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_PREPARE
	const apply = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY
	const remove = nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_REMOVE
	action := req.GetAction()
	if action != prepare && action != apply && action != remove {
		return nil, status.Error(codes.InvalidArgument, "invalid transparent redirect action")
	}
	if action != remove {
		if err := validateRedirectRequest(req); err != nil {
			return nil, err
		}
	}

	path := redirectStatePath(s.config)
	table := nodegrpc.TransparentRedirectTableName(s.config.NodeID)
	nftApplyMu.Lock()
	defer nftApplyMu.Unlock()
	current, err := readRedirectState(path)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "read transparent redirect state: %v", err)
	}
	active := current.Owner == req.GetOwner()
	if action == remove && !active {
		return &nodegrpc.TransparentRedirectReply{}, nil
	}
	if req.GetRequireActive() && !active {
		return nil, status.Error(codes.NotFound, "transparent redirect is no longer active")
	}
	if current.Owner != "" && !active {
		return nil, status.Errorf(codes.AlreadyExists, "transparent redirect on this node is used by %s", current.Owner)
	}
	if action == prepare && active {
		return current.reply(true), nil
	}

	nft, ruleset, err := redirectRuleset(ctx)
	if err != nil {
		return nil, err
	}
	live, err := inspectRedirectTable(ruleset, table, current, action != remove)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if action == remove {
		next := redirectState{Version: redirectRulesVersion, Revision: current.Revision + 1}
		return &nodegrpc.TransparentRedirectReply{}, writeRedirectState(ctx, nft, path, table, current, next)
	}

	next, err := prepareRedirectState(ctx, ruleset, table, req, current, active)
	if err != nil {
		return nil, err
	}
	if action == prepare {
		return next.reply(false), nil
	}
	if req.GetMark() != next.Mark || req.GetDockerAddress() != redirectAddress(next.DockerAddress) {
		return nil, status.Error(codes.FailedPrecondition, "transparent redirect plan changed; retry tunnel start")
	}
	if current.sameRules(next) {
		if !live {
			if err := applyRedirectBatch(ctx, nft, current.batch(table)); err != nil {
				return nil, err
			}
		}
		return current.reply(true), nil
	}
	if err := writeRedirectState(ctx, nft, path, table, current, next); err != nil {
		return nil, err
	}
	return next.reply(true), nil
}

func validateRedirectRequest(req *nodegrpc.TransparentRedirectRequest) error {
	if req.GetPort() == 0 || req.GetPort() > 65535 || req.GetRelayHost() == "" && len(req.GetRelayHosts()) == 0 {
		return status.Error(codes.InvalidArgument, "invalid transparent redirect port or Relay address")
	}
	if len(req.GetRelayHost()) > 253 {
		return status.Error(codes.InvalidArgument, "invalid transparent redirect Relay address")
	}
	for _, host := range req.GetRelayHosts() {
		if host == "" || len(host) > 253 {
			return status.Error(codes.InvalidArgument, "invalid transparent redirect Relay address")
		}
	}
	return nil
}

func redirectRelayHosts(req *nodegrpc.TransparentRedirectRequest) []string {
	hosts := slices.Clone(req.GetRelayHosts())
	if host := req.GetRelayHost(); host != "" {
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

func prepareRedirectState(ctx context.Context, ruleset []byte, table string, req *nodegrpc.TransparentRedirectRequest, current redirectState, active bool) (redirectState, error) {
	preferred := uint32(gost.ClashRedirectMark)
	if active {
		preferred = current.Mark
	}
	mark, err := selectRedirectMark(ruleset, table, preferred)
	if err != nil {
		return redirectState{}, status.Errorf(codes.FailedPrecondition, "select transparent redirect mark: %v", err)
	}
	if active && mark != current.Mark {
		return redirectState{}, status.Error(codes.FailedPrecondition, "the active transparent redirect mark overlaps existing nftables rules; restart the tunnel to select a new mark")
	}
	relayIPs, err := redirectRelayIPs(ctx, redirectRelayHosts(req)...)
	if err != nil {
		return redirectState{}, redirectNftError(ctx, "resolve Relay address", err)
	}
	next := redirectState{
		Version: redirectRulesVersion, Revision: current.Revision + 1,
		Owner: req.GetOwner(), Port: req.GetPort(), Mark: mark, RelayIPs: relayIPs,
	}
	next.DockerAddress = current.DockerAddress
	if err := next.detectDocker(ctx, !active); err != nil {
		return redirectState{}, err
	}
	return next, nil
}

func (s *redirectState) detectDocker(ctx context.Context, selectAddress bool) error {
	if !selectAddress && !s.DockerAddress.IsValid() {
		return nil
	}
	bridges, err := discoverDockerBridges(ctx)
	if err != nil {
		return redirectNftError(ctx, "detect Docker bridge networks", err)
	}
	if selectAddress && len(bridges) != 0 {
		s.DockerAddress = bridges[0].Address
	}
	if !s.DockerAddress.IsValid() {
		return nil
	}
	if !slices.ContainsFunc(bridges, func(bridge dockerBridge) bool { return bridge.Address == s.DockerAddress }) {
		// Gost keeps the listener bound until its bridge returns. Only the host redirect can run meanwhile.
		s.Bridges = nil
		return nil
	}
	s.Bridges = make([]string, 0, len(bridges))
	for _, bridge := range bridges {
		s.Bridges = append(s.Bridges, bridge.Name)
	}
	return nil
}

func redirectRelayIPs(ctx context.Context, hosts ...string) ([]netip.Addr, error) {
	var ips []netip.Addr
	for _, host := range hosts {
		address, err := netip.ParseAddr(host)
		addresses := []netip.Addr{address}
		if err != nil {
			addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
		}
		for _, address := range addresses {
			if address.Unmap().Is4() {
				ips = append(ips, address.Unmap())
			}
		}
	}
	slices.SortFunc(ips, netip.Addr.Compare)
	return slices.Compact(ips), nil
}

func redirectStatePath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.ConfigFile), "transparent-redirect.json")
}

func readRedirectState(path string) (redirectState, error) {
	var state redirectState
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Version > redirectRulesVersion {
		return state, fmt.Errorf("rule version %d is newer than supported version %d", state.Version, redirectRulesVersion)
	}
	if state.Owner != "" && (!redirectOwnerName.MatchString(state.Owner) || state.Port == 0 || state.Port > 65535 || state.Mark == 0 ||
		(state.DockerAddress.IsValid() && !state.DockerAddress.Is4())) {
		return state, errors.New("invalid saved transparent redirect")
	}
	return state, nil
}

func (s redirectState) sameRules(other redirectState) bool {
	return s.Version == other.Version && s.Owner == other.Owner && s.Port == other.Port && s.Mark == other.Mark &&
		s.DockerAddress == other.DockerAddress && slices.Equal(s.RelayIPs, other.RelayIPs) && slices.Equal(s.Bridges, other.Bridges)
}

func (s redirectState) reply(active bool) *nodegrpc.TransparentRedirectReply {
	return &nodegrpc.TransparentRedirectReply{Mark: s.Mark, DockerAddress: redirectAddress(s.DockerAddress), Active: active, MultiRelaySupported: true}
}

func redirectAddress(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

func (s redirectState) batch(table string) []byte {
	return redirectNftBatch(table, s.Port, s.Mark, s.RelayIPs, s.DockerAddress, s.Bridges, s.Version, s.Revision)
}

func redirectRuleset(ctx context.Context) (string, []byte, error) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return "", nil, status.Error(codes.Unimplemented, "nftables is unavailable: install the nftables package on this node")
	}
	ruleset, err := nftOutput(ctx, nft, nil, "-j", "-nn", "list", "ruleset")
	if err != nil {
		return "", nil, redirectNftError(ctx, "read nftables rules", err)
	}
	return nft, ruleset, nil
}

func inspectRedirectTable(ruleset []byte, table string, current redirectState, checkManual bool) (bool, error) {
	var listing struct {
		Objects []nftObject `json:"nftables"`
	}
	if err := json.Unmarshal(ruleset, &listing); err != nil {
		return false, err
	}
	var currentVersion bool
	var manualRedirect error
	for _, object := range listing.Objects {
		if raw := object["table"]; raw != nil {
			var entry struct {
				Family  string `json:"family"`
				Name    string `json:"name"`
				Comment string `json:"comment"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				return false, err
			}
			if entry.Family == "inet" && entry.Name == table {
				if current.Owner == "" || entry.Comment != redirectTableComment(current.Version, current.Revision) {
					return false, fmt.Errorf("existing table inet %s does not match the saved transparent redirect revision", table)
				}
				currentVersion = true
			}
		}
		if !checkManual || object["rule"] == nil {
			continue
		}
		var rule struct {
			Family string      `json:"family"`
			Table  string      `json:"table"`
			Expr   []nftObject `json:"expr"`
		}
		if err := json.Unmarshal(object["rule"], &rule); err != nil {
			return false, err
		}
		if (rule.Family == "inet" || rule.Family == "ip") && rule.Table == "gost" {
			for _, expr := range rule.Expr {
				if expr["redirect"] != nil || expr["dnat"] != nil {
					manualRedirect = fmt.Errorf("existing table %s gost contains manual redirection; remove the duplicate before starting this tunnel", rule.Family)
					break
				}
			}
		}
	}
	return currentVersion, manualRedirect
}

func writeRedirectState(ctx context.Context, nft, path, table string, current, next redirectState) error {
	encoded, err := json.Marshal(next)
	if err != nil {
		return status.Errorf(codes.Internal, "encode transparent redirect state: %v", err)
	}
	staged, err := stageNftConfig(path, string(encoded))
	if err != nil {
		return status.Errorf(codes.Internal, "stage transparent redirect state: %v", err)
	}
	defer os.Remove(staged)
	if err := applyRedirectBatch(ctx, nft, next.batch(table)); err != nil {
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return status.Errorf(codes.Internal, "save transparent redirect state: %v", errors.Join(err, runNft(recovery, nft, current.batch(table), "-f", "-")))
	}
	return nil
}

func applyRedirectBatch(ctx context.Context, nft string, batch []byte) error {
	if err := runNft(ctx, nft, batch, "-c", "-f", "-"); err != nil {
		return redirectNftError(ctx, "check transparent redirect rules", err)
	}
	if err := runNft(ctx, nft, batch, "-f", "-"); err != nil {
		return redirectNftError(ctx, "apply transparent redirect rules", err)
	}
	return nil
}

func redirectNftError(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Errorf(codes.FailedPrecondition, "%s: %v", op, err)
}

func (s *controlServer) restoreTransparentRedirect(ctx context.Context) error {
	path := redirectStatePath(s.config)
	current, err := readRedirectState(path)
	if err != nil || current.Owner == "" {
		return err
	}
	if err := WaitForGost(ctx, s.config); err != nil {
		return err
	}
	if err := s.checkRedirectService(ctx, current.Owner, "127.0.0.1", current); err != nil {
		return err
	}
	if current.DockerAddress.IsValid() {
		if err := s.checkRedirectService(ctx, current.Owner+"-docker", current.DockerAddress.String(), current); err != nil {
			return err
		}
	}

	table := nodegrpc.TransparentRedirectTableName(s.config.NodeID)
	nftApplyMu.Lock()
	defer nftApplyMu.Unlock()
	nft, ruleset, err := redirectRuleset(ctx)
	if err != nil {
		return err
	}
	live, err := inspectRedirectTable(ruleset, table, current, true)
	if err != nil {
		return suspendRedirectOnConflict(ctx, nft, table, live, err)
	}
	mark, err := selectRedirectMark(ruleset, table, current.Mark)
	if err != nil {
		return suspendRedirectOnConflict(ctx, nft, table, live, err)
	}
	if mark != current.Mark {
		conflict := errors.New("the saved transparent redirect mark overlaps existing nftables rules; restart the tunnel to select a new mark")
		return suspendRedirectOnConflict(ctx, nft, table, live, conflict)
	}
	next := current
	next.Version, next.Revision = redirectRulesVersion, current.Revision+1
	if err := next.detectDocker(ctx, false); err != nil {
		return err
	}
	if current.sameRules(next) {
		if live {
			return nil
		}
		return applyRedirectBatch(ctx, nft, current.batch(table))
	}
	return writeRedirectState(ctx, nft, path, table, current, next)
}

func suspendRedirectOnConflict(ctx context.Context, nft, table string, live bool, conflict error) error {
	if live {
		return errors.Join(conflict, applyRedirectBatch(ctx, nft, (redirectState{}).batch(table)))
	}
	return conflict
}

func (s *controlServer) checkRedirectService(ctx context.Context, name, address string, state redirectState) error {
	reply, err := s.Execute(ctx, &nodegrpc.CommandRequest{Method: http.MethodGet, Path: "/config/services/" + name})
	if err != nil {
		return err
	}
	var result struct {
		Data gost.ServiceConfig `json:"data"`
	}
	if reply.StatusCode != http.StatusOK || json.Unmarshal(reply.Body, &result) != nil {
		return fmt.Errorf("Gost transparent service %s is unavailable", name)
	}
	service := result.Data
	if service.Addr != net.JoinHostPort(address, strconv.Itoa(int(state.Port))) ||
		service.Listener == nil || service.Listener.Type != "red" ||
		service.Handler == nil || service.Handler.Type != "red" ||
		(service.Handler.Chain == "" && (service.Handler.ChainGroup == nil || len(service.Handler.ChainGroup.Chains) == 0)) ||
		service.SockOpts == nil || uint32(service.SockOpts.Mark) != state.Mark {
		return fmt.Errorf("Gost transparent service %s does not match the saved nftables configuration", name)
	}
	return nil
}
