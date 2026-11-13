package nodebridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var errUFWInactive = errors.New("automatic port allowance requires UFW to be enabled and active")

type ufwCommand func(ctx context.Context, executable string, batch []byte, args ...string) ([]byte, error)

type ufwBackend struct {
	run      ufwCommand
	readFile func(string) ([]byte, error)
	systemd  bool
	ipv6     bool
}

func (m *FirewallManager) reconcileUFW(ctx context.Context, executable string, ports FirewallPorts) error {
	_, systemd := os.Stat("/run/systemd/system")
	_, ipv6 := os.Stat("/proc/sys/net/ipv6")
	backend := ufwBackend{run: runUFWCommand, readFile: os.ReadFile, systemd: systemd == nil, ipv6: ipv6 == nil}
	return backend.reconcile(ctx, executable, m.owner, ports)
}

func runUFWCommand(ctx context.Context, executable string, batch []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	cmd.Stdin = bytes.NewReader(batch)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		detail := strings.TrimSpace(string(exit.Stderr))
		if len(detail) > 2048 {
			detail = strings.ToValidUTF8(detail[:2048], "�") + "…"
		}
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
	}
	return nil, err
}

func (b ufwBackend) reconcile(ctx context.Context, executable, owner string, ports FirewallPorts) error {
	ipv6, err := b.active(ctx, executable)
	if err != nil {
		return err
	}
	const v4Chain = "ufw-user-input"
	const v6Chain = "ufw6-user-input"
	desired, unavailable, invalid := ufwEndpoints(ports, ipv6)
	if invalid != nil {
		return invalid
	}
	v4, err := b.plan(ctx, "iptables", v4Chain, owner, desired[0])
	if err != nil {
		return err
	}
	plans := []ufwPlan{v4}
	if ipv6 {
		v6, err := b.plan(ctx, "ip6tables", v6Chain, owner, desired[1])
		if err != nil {
			return err
		}
		plans = append(plans, v6)
	}
	return errors.Join(b.apply(ctx, plans), unavailable)
}

func (b ufwBackend) active(ctx context.Context, executable string) (bool, error) {
	config, err := b.readFile("/etc/ufw/ufw.conf")
	if err != nil {
		return false, fmt.Errorf("read UFW configuration: %w", err)
	}
	if ufwSetting(config, "ENABLED") != "yes" {
		return false, errUFWInactive
	}
	if b.systemd {
		state, err := b.run(ctx, "systemctl", nil, "is-enabled", "ufw.service")
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err != nil || strings.TrimSpace(string(state)) != "enabled" {
			return false, fmt.Errorf("%w: ufw.service must be enabled", errUFWInactive)
		}
	}
	status, err := b.run(ctx, executable, nil, "status")
	if err != nil {
		return false, fmt.Errorf("read UFW status: %w", err)
	}
	line, _, _ := strings.Cut(string(status), "\n")
	if strings.TrimSpace(line) != "Status: active" {
		return false, errUFWInactive
	}
	defaults, err := b.readFile("/etc/default/ufw")
	if err != nil {
		return false, fmt.Errorf("read UFW defaults: %w", err)
	}
	return b.ipv6 && ufwSetting(defaults, "IPV6") == "yes", nil
}

func ufwSetting(config []byte, name string) string {
	var setting string
	for line := range strings.SplitSeq(string(config), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found && key == name {
			setting = strings.ToLower(strings.Trim(strings.TrimSpace(value), `"'`))
		}
	}
	return setting
}

type ufwEndpoint struct {
	address  netip.Addr
	port     uint16
	protocol string
}

func ufwEndpoints(ports FirewallPorts, ipv6 bool) (endpoints [2][]ufwEndpoint, unavailable, invalid error) {
	for _, protocol := range []struct {
		name      string
		wildcard  []uint16
		endpoints []FirewallEndpoint
	}{
		{"tcp", ports.TCP, ports.TCPAddresses},
		{"udp", ports.UDP, ports.UDPAddresses},
	} {
		for _, port := range protocol.wildcard {
			if port == 0 {
				return endpoints, nil, errors.New("UFW port must be nonzero")
			}
			endpoint := ufwEndpoint{port: port, protocol: protocol.name}
			endpoints[0] = append(endpoints[0], endpoint)
			if ipv6 {
				endpoints[1] = append(endpoints[1], endpoint)
			}
		}
		for _, endpoint := range protocol.endpoints {
			address := endpoint.Address.Unmap()
			if !address.IsValid() || address.IsUnspecified() || address.Zone() != "" || endpoint.Port == 0 {
				return endpoints, nil, fmt.Errorf("invalid UFW endpoint %s port %d", endpoint.Address, endpoint.Port)
			}
			family := 0
			if address.Is6() {
				if !ipv6 {
					if unavailable == nil {
						unavailable = fmt.Errorf("UFW IPv6 support is disabled for listener %s", netip.AddrPortFrom(address, endpoint.Port))
					}
					continue
				}
				family = 1
			}
			if !slices.Contains(protocol.wildcard, endpoint.Port) {
				endpoints[family] = append(endpoints[family], ufwEndpoint{address, endpoint.Port, protocol.name})
			}
		}
	}
	return endpoints, unavailable, nil
}

// iptables -S renders a bare allow with the address (if any), protocol module,
// destination port and verdict. Any additional conditions belong to host policy.
var ufwSimpleRule = regexp.MustCompile(`^(?:-d ([0-9A-Fa-f.:/]+) )?-p (tcp|udp) -m (tcp|udp) --dport ([0-9]+)(?: -m comment --comment ("[^"\\]*"|[^ ]+))? -j (ACCEPT|DROP|REJECT)(?: --reject-with [a-z-]+)?$`)

func ufwParseRule(rule string) (ufwEndpoint, string, string, bool) {
	match := ufwSimpleRule.FindStringSubmatch(rule)
	if match == nil || match[2] != match[3] {
		return ufwEndpoint{}, "", "", false
	}
	var address netip.Addr
	if match[1] != "" {
		prefix, err := netip.ParsePrefix(match[1])
		if err != nil || prefix.Bits() != prefix.Addr().BitLen() {
			return ufwEndpoint{}, "", "", false
		}
		address = prefix.Addr()
	}
	port, err := strconv.ParseUint(match[4], 10, 16)
	if err != nil || port == 0 {
		return ufwEndpoint{}, "", "", false
	}
	return ufwEndpoint{address, uint16(port), match[2]}, strings.Trim(match[5], `"`), match[6], true
}

func (endpoint ufwEndpoint) rule(owner string) string {
	var address string
	if endpoint.address.IsValid() {
		address = "-d " + netip.PrefixFrom(endpoint.address, endpoint.address.BitLen()).String() + " "
	}
	return fmt.Sprintf("%s-p %s -m %s --dport %d -m comment --comment %q -j ACCEPT", address, endpoint.protocol, endpoint.protocol, endpoint.port, owner)
}

type ufwPlan struct {
	restore  string
	batch    []byte
	rollback []byte
}

type ufwRule struct {
	rule     string
	position int
	endpoint ufwEndpoint
	accept   bool
}

func (b ufwBackend) plan(ctx context.Context, executable, chain, owner string, desired []ufwEndpoint) (ufwPlan, error) {
	live, err := b.run(ctx, executable, nil, "--wait", "2", "-t", "filter", "-S")
	if err != nil {
		return ufwPlan{}, fmt.Errorf("read %s UFW rules: %w", executable, err)
	}
	available := make(map[ufwEndpoint]bool)
	denied := make(map[ufwEndpoint]bool)
	var owned []ufwRule
	var input, delegated bool
	before := strings.Replace(chain, "-user-", "-before-", 1)
	prefix := "-A " + chain + " "
	position, lastHostRule := 0, 0
	for line := range strings.SplitSeq(string(live), "\n") {
		switch line {
		case "-A INPUT -j " + before:
			input = true
		case "-A " + before + " -j " + chain:
			delegated = true
		}
		rule, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		position++
		endpoint, comment, action, parsed := ufwParseRule(rule)
		if !parsed && strings.Contains(rule, "-m comment --comment "+strconv.Quote(owner)+" ") {
			return ufwPlan{}, fmt.Errorf("UFW chain %s contains an unrecognized rule marked %q", chain, owner)
		}
		if parsed && comment == owner {
			owned = append(owned, ufwRule{rule: rule, position: position, endpoint: endpoint, accept: action == "ACCEPT"})
		} else {
			lastHostRule = position
			if parsed {
				if action == "ACCEPT" {
					available[endpoint] = true
				} else {
					denied[endpoint] = true
				}
			}
		}
	}
	if !input || !delegated {
		return ufwPlan{}, fmt.Errorf("%w: no active %s input delegation", errUFWInactive, chain)
	}
	var remove []ufwRule
	for _, rule := range owned {
		if !rule.accept || available[rule.endpoint] || denied[rule.endpoint] || rule.position < lastHostRule || !slices.Contains(desired, rule.endpoint) {
			remove = append(remove, rule)
		} else {
			available[rule.endpoint] = true
		}
	}
	var add []string
	for _, endpoint := range desired {
		if !available[endpoint] && !denied[endpoint] {
			add = append(add, endpoint.rule(owner))
			available[endpoint] = true
		}
	}
	return makeUFWPlan(executable, chain, add, remove), nil
}

func makeUFWPlan(executable, chain string, add []string, remove []ufwRule) ufwPlan {
	plan := ufwPlan{restore: executable + "-restore"}
	if len(add)+len(remove) == 0 {
		return plan
	}
	var batch, rollback strings.Builder
	batch.WriteString("*filter\n")
	rollback.WriteString("*filter\n")
	for _, rule := range remove {
		fmt.Fprintf(&batch, "-D %s %s\n", chain, rule.rule)
	}
	for _, rule := range add {
		fmt.Fprintf(&batch, "-A %s %s\n", chain, rule)
		fmt.Fprintf(&rollback, "-D %s %s\n", chain, rule)
	}
	for _, rule := range remove {
		fmt.Fprintf(&rollback, "-I %s %d %s\n", chain, rule.position, rule.rule)
	}
	batch.WriteString("COMMIT\n")
	rollback.WriteString("COMMIT\n")
	plan.batch = []byte(batch.String())
	plan.rollback = []byte(rollback.String())
	return plan
}

func (b ufwBackend) apply(ctx context.Context, plans []ufwPlan) error {
	// UFW CLI deletions flush its user chains before reloading. Apply only the
	// owned rules in no-flush transactions against the same iptables backend.
	for _, plan := range plans {
		if len(plan.batch) != 0 {
			if _, err := b.run(ctx, plan.restore, plan.batch, "--wait", "2", "--noflush", "--test"); err != nil {
				return fmt.Errorf("check UFW rules with %s: %w", plan.restore, err)
			}
		}
	}
	var applied []ufwPlan
	for _, plan := range plans {
		if len(plan.batch) == 0 {
			continue
		}
		if _, err := b.run(ctx, plan.restore, plan.batch, "--wait", "2", "--noflush"); err != nil {
			err = fmt.Errorf("apply UFW rules with %s: %w", plan.restore, err)
			return errors.Join(err, b.rollback(ctx, applied))
		}
		applied = append(applied, plan)
	}
	return nil
}

func (b ufwBackend) rollback(ctx context.Context, plans []ufwPlan) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var result error
	for _, plan := range slices.Backward(plans) {
		if _, err := b.run(ctx, plan.restore, plan.rollback, "--wait", "2", "--noflush"); err != nil {
			result = errors.Join(result, fmt.Errorf("restore previous UFW rules with %s: %w", plan.restore, err))
		}
	}
	return result
}
