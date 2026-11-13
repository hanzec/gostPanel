package nodebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FirewallConfig identifies the host nftables input chain that delegates YAGP traffic.
type FirewallConfig struct {
	Table      string `json:"table" yaml:"table"`
	InputChain string `json:"input_chain" yaml:"input_chain"`
}

type FirewallEndpoint struct {
	Address netip.Addr
	Port    uint16
}

type FirewallPorts struct {
	TCP, UDP                   []uint16
	TCPAddresses, UDPAddresses []FirewallEndpoint
}

type FirewallStatus struct {
	Supported    bool       `json:"supported"`
	Managed      bool       `json:"managed"`
	Synchronized bool       `json:"synchronized"`
	LastSync     *time.Time `json:"last_sync"`
	LastError    *string    `json:"last_error"`
}

type FirewallManager struct {
	config    FirewallConfig
	configErr error
	prefix    string
	owner     string

	statusMu sync.RWMutex
	status   FirewallStatus
}

var (
	firewallIdentifier     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,254}$`)
	errFirewallUnsupported = errors.New("firewall synchronization requires nftables on Linux")
)

func NewFirewallManager(nodeID uint64, config FirewallConfig) (*FirewallManager, error) {
	manager := &FirewallManager{
		config: config,
		prefix: fmt.Sprintf("yagp_%d_", nodeID),
		owner:  fmt.Sprintf("yagp daemon firewall %d", nodeID),
	}
	_, manager.status.Supported = findFirewallNft()
	manager.status.Managed = config != (FirewallConfig{})
	if manager.status.Managed && (!firewallIdentifier.MatchString(config.Table) ||
		!firewallIdentifier.MatchString(config.InputChain) || config.InputChain == manager.prefix+"input") {
		manager.configErr = errors.New("firewall requires an existing nftables inet table and input base chain with valid names")
		manager.recordSyncError(manager.configErr)
	} else if manager.status.Managed && !manager.status.Supported {
		manager.recordSyncError(errFirewallUnsupported)
	}
	return manager, manager.configErr
}

func findFirewallNft() (string, bool) {
	if runtime.GOOS != "linux" {
		return "", false
	}
	nft, err := exec.LookPath("nft")
	return nft, err == nil
}

func (m *FirewallManager) Status() FirewallStatus {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	status := m.status
	if status.LastSync != nil {
		value := *status.LastSync
		status.LastSync = &value
	}
	if status.LastError != nil {
		value := *status.LastError
		status.LastError = &value
	}
	return status
}

func (m *FirewallManager) recordSyncError(err error) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if m.configErr != nil {
		err = m.configErr
	}
	m.status.Synchronized = false
	detail := err.Error()
	m.status.LastError = &detail
}

func (m *FirewallManager) Reconcile(ctx context.Context, ports FirewallPorts) error {
	if !m.status.Managed || m.configErr != nil {
		return m.configErr
	}

	nftApplyMu.Lock()
	defer nftApplyMu.Unlock()
	nft, supported := findFirewallNft()
	err := ctx.Err()
	if err == nil {
		if supported {
			err = m.reconcile(ctx, nft, ports)
		} else {
			err = errFirewallUnsupported
		}
	}

	m.statusMu.Lock()
	m.status.Supported = supported
	m.status.Synchronized = err == nil
	if err == nil {
		now := time.Now().UTC()
		m.status.LastSync, m.status.LastError = &now, nil
	} else {
		detail := err.Error()
		m.status.LastError = &detail
	}
	m.statusMu.Unlock()
	return err
}

func (m *FirewallManager) reconcile(ctx context.Context, nft string, ports FirewallPorts) error {
	sets, err := m.desiredSets(ports)
	if err != nil {
		return err
	}
	output, err := nftOutput(ctx, nft, nil, "-j", "-nn", "list", "table", "inet", m.config.Table)
	if err != nil {
		return fmt.Errorf("read firewall table inet %s: %w", m.config.Table, err)
	}
	live, err := m.readRuleset(output, sets)
	if err != nil {
		return err
	}
	batch := m.batch(live, sets)
	if len(batch) > 0 {
		if err := runNft(ctx, nft, batch, "-c", "-f", "-"); err != nil {
			return fmt.Errorf("check firewall update: %w", err)
		}
		if err := runNft(ctx, nft, batch, "-f", "-"); err != nil {
			return fmt.Errorf("apply firewall update: %w", err)
		}
	}
	if !live.delegated {
		return fmt.Errorf("firewall input chain inet %s %s needs an unconditional jump to %s before its deny rules", m.config.Table, m.config.InputChain, m.prefix+"input")
	}
	return nil
}

type firewallSet struct {
	name     string
	protocol string
	family   string
	elements []string
}

func (s firewallSet) nftType() string {
	switch s.family {
	case "ip":
		return "ipv4_addr . inet_service"
	case "ip6":
		return "ipv6_addr . inet_service"
	default:
		return "inet_service"
	}
}

func (s firewallSet) match() string {
	match := s.protocol + " dport"
	if s.family != "" {
		match = s.family + " daddr . " + match
	}
	return match
}

func (m *FirewallManager) desiredSets(ports FirewallPorts) ([]firewallSet, error) {
	var sets []firewallSet
	for _, protocol := range []struct {
		name      string
		wildcard  []uint16
		endpoints []FirewallEndpoint
	}{
		{"tcp", ports.TCP, ports.TCPAddresses},
		{"udp", ports.UDP, ports.UDPAddresses},
	} {
		wildcard := firewallSet{name: m.prefix + protocol.name, protocol: protocol.name}
		v4 := firewallSet{name: wildcard.name + "4", protocol: protocol.name, family: "ip"}
		v6 := firewallSet{name: wildcard.name + "6", protocol: protocol.name, family: "ip6"}
		for _, port := range protocol.wildcard {
			if port == 0 {
				return nil, errors.New("firewall port must be nonzero")
			}
			wildcard.elements = append(wildcard.elements, strconv.Itoa(int(port)))
		}
		for _, endpoint := range protocol.endpoints {
			address := endpoint.Address.Unmap()
			if !address.IsValid() || address.IsUnspecified() || address.Zone() != "" || endpoint.Port == 0 {
				return nil, fmt.Errorf("invalid firewall endpoint %v port %d", endpoint.Address, endpoint.Port)
			}
			set := &v6
			if address.Is4() {
				set = &v4
			}
			set.elements = append(set.elements, firewallElement(address.String(), int(endpoint.Port)))
		}
		sets = append(sets, wildcard, v4, v6)
	}
	for i := range sets {
		slices.Sort(sets[i].elements)
		sets[i].elements = slices.Compact(sets[i].elements)
	}
	return sets, nil
}

func firewallElement(address string, port int) string {
	return address + " . " + strconv.Itoa(port)
}

type firewallObject struct {
	Name     string            `json:"name"`
	Comment  string            `json:"comment"`
	Hook     string            `json:"hook"`
	Type     json.RawMessage   `json:"type"`
	Flags    []string          `json:"flags"`
	Elements []json.RawMessage `json:"elem"`
}

type firewallRule struct {
	Chain string                       `json:"chain"`
	Expr  []map[string]json.RawMessage `json:"expr"`
}

type firewallRuleset struct {
	chain     bool
	delegated bool
	sets      map[string]firewallObject
	rules     []firewallRule
}

func (m *FirewallManager) readRuleset(output []byte, desired []firewallSet) (firewallRuleset, error) {
	var document struct {
		Objects []struct {
			Table *firewallObject `json:"table"`
			Chain *firewallObject `json:"chain"`
			Set   *firewallObject `json:"set"`
			Map   *firewallObject `json:"map"`
			Rule  *firewallRule   `json:"rule"`
		} `json:"nftables"`
	}
	live := firewallRuleset{sets: make(map[string]firewallObject, len(desired))}
	if err := json.Unmarshal(output, &document); err != nil {
		return live, fmt.Errorf("decode nftables firewall rules: %w", err)
	}
	chainName := m.prefix + "input"
	var table, input bool
	var delegationBlocked bool
	for _, object := range document.Objects {
		switch {
		case object.Table != nil && object.Table.Name == m.config.Table:
			if slices.Contains(object.Table.Flags, "dormant") {
				return live, fmt.Errorf("firewall table inet %s is dormant", m.config.Table)
			}
			table = true
		case object.Chain != nil && object.Chain.Name == m.config.InputChain:
			input = object.Chain.Hook == "input" && string(object.Chain.Type) == `"filter"`
		case object.Chain != nil && object.Chain.Name == chainName:
			if object.Chain.Comment != m.owner || object.Chain.Hook != "" {
				return live, fmt.Errorf("firewall chain %s conflicts with an existing nftables chain", chainName)
			}
			live.chain = true
		case object.Set != nil && slices.ContainsFunc(desired, func(set firewallSet) bool { return set.name == object.Set.Name }):
			if object.Set.Comment != m.owner {
				return live, fmt.Errorf("firewall set %s conflicts with an existing nftables set", object.Set.Name)
			}
			live.sets[object.Set.Name] = *object.Set
		case object.Map != nil && slices.ContainsFunc(desired, func(set firewallSet) bool { return set.name == object.Map.Name }):
			return live, fmt.Errorf("firewall set %s conflicts with an existing nftables map", object.Map.Name)
		case object.Rule != nil && object.Rule.Chain == chainName:
			live.rules = append(live.rules, *object.Rule)
		case object.Rule != nil && object.Rule.Chain == m.config.InputChain && !delegationBlocked:
			statement, value := object.Rule.unconditionalVerdict()
			switch statement {
			case "jump":
				var jump struct {
					Target string `json:"target"`
				}
				if json.Unmarshal(value, &jump) == nil && jump.Target == chainName {
					live.delegated = true
				}
			case "drop", "reject", "return", "accept", "goto":
				delegationBlocked = true
			}
		}
	}
	if !table || !input {
		return live, fmt.Errorf("firewall requires existing filter input base chain inet %s %s", m.config.Table, m.config.InputChain)
	}
	return live, nil
}

func (r firewallRule) unconditionalVerdict() (string, json.RawMessage) {
	for _, expr := range r.Expr {
		if len(expr) != 1 {
			return "", nil
		}
		for statement, value := range expr {
			switch statement {
			case "counter", "log", "comment":
			case "jump", "drop", "reject", "return", "accept", "goto":
				return statement, value
			default:
				return "", nil
			}
		}
	}
	return "", nil
}

func (s firewallSet) matchesRule(rule firewallRule) bool {
	if len(rule.Expr) != 2 || len(rule.Expr[0]) != 1 || len(rule.Expr[1]) != 1 || string(rule.Expr[1]["accept"]) != "null" {
		return false
	}
	var match struct {
		Left  any    `json:"left"`
		Op    string `json:"op"`
		Right string `json:"right"`
	}
	if err := json.Unmarshal(rule.Expr[0]["match"], &match); err != nil ||
		(match.Op != "==" && match.Op != "in") || match.Right != "@"+s.name {
		return false
	}
	payload := func(protocol, field string) map[string]any {
		return map[string]any{"payload": map[string]any{"protocol": protocol, "field": field}}
	}
	var expected any = payload(s.protocol, "dport")
	if s.family != "" {
		expected = map[string]any{"concat": []any{payload(s.family, "daddr"), expected}}
	}
	return reflect.DeepEqual(match.Left, expected)
}

func (s firewallSet) compatible(live firewallObject) bool {
	var dataType any
	if len(live.Flags) != 0 || json.Unmarshal(live.Type, &dataType) != nil {
		return false
	}
	if s.family == "" {
		return dataType == "inet_service"
	}
	expected := strings.Split(s.nftType(), " . ")
	return reflect.DeepEqual(dataType, []any{expected[0], expected[1]})
}

func (s firewallSet) liveElements(live firewallObject) ([]string, bool) {
	var elements []string
	for _, raw := range live.Elements {
		if s.family == "" {
			var port uint16
			if err := json.Unmarshal(raw, &port); err != nil || port == 0 {
				return nil, false
			}
			elements = append(elements, strconv.Itoa(int(port)))
			continue
		}
		var element struct {
			Concat []json.RawMessage `json:"concat"`
		}
		var address netip.Addr
		var port uint16
		if json.Unmarshal(raw, &element) != nil || len(element.Concat) != 2 ||
			json.Unmarshal(element.Concat[0], &address) != nil || !address.IsValid() ||
			json.Unmarshal(element.Concat[1], &port) != nil || port == 0 {
			return nil, false
		}
		elements = append(elements, firewallElement(address.String(), int(port)))
	}
	slices.Sort(elements)
	return elements, true
}

func (m *FirewallManager) batch(live firewallRuleset, sets []firewallSet) []byte {
	var batch strings.Builder
	table := m.config.Table
	chain := m.prefix + "input"
	rebuildRules := !live.chain || len(live.rules) != len(sets)
	for i, set := range sets {
		current, exists := live.sets[set.name]
		if !exists || !set.compatible(current) || (!rebuildRules && !set.matchesRule(live.rules[i])) {
			rebuildRules = true
		}
	}
	if !live.chain {
		fmt.Fprintf(&batch, "create chain inet %s %s { comment %q; }\n", table, chain, m.owner)
	} else if rebuildRules {
		fmt.Fprintf(&batch, "flush chain inet %s %s\n", table, chain)
	}
	for _, set := range sets {
		current, exists := live.sets[set.name]
		if exists && set.compatible(current) {
			appendFirewallSetUpdate(&batch, table, set, current)
			continue
		}
		if exists {
			fmt.Fprintf(&batch, "delete set inet %s %s\n", table, set.name)
		}
		fmt.Fprintf(&batch, "create set inet %s %s { type %s; comment %q; }\n", table, set.name, set.nftType(), m.owner)
		appendFirewallElements(&batch, "add", table, set.name, set.elements)
	}
	if rebuildRules {
		for _, set := range sets {
			fmt.Fprintf(&batch, "add rule inet %s %s %s @%s accept\n", table, chain, set.match(), set.name)
		}
	}
	return []byte(batch.String())
}

func appendFirewallSetUpdate(batch *strings.Builder, table string, set firewallSet, current firewallObject) {
	live, valid := set.liveElements(current)
	if !valid {
		fmt.Fprintf(batch, "flush set inet %s %s\n", table, set.name)
		appendFirewallElements(batch, "add", table, set.name, set.elements)
		return
	}
	var additions, removals []string
	for _, element := range set.elements {
		if _, exists := slices.BinarySearch(live, element); !exists {
			additions = append(additions, element)
		}
	}
	for _, element := range live {
		if _, exists := slices.BinarySearch(set.elements, element); !exists {
			removals = append(removals, element)
		}
	}
	appendFirewallElements(batch, "delete", table, set.name, removals)
	appendFirewallElements(batch, "add", table, set.name, additions)
}

func appendFirewallElements(batch *strings.Builder, verb, table, set string, elements []string) {
	if len(elements) > 0 {
		fmt.Fprintf(batch, "%s element inet %s %s { %s }\n", verb, table, set, strings.Join(elements, ", "))
	}
}
