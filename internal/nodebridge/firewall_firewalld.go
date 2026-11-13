package nodebridge

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	firewalldService = "org.fedoraproject.FirewallD1"
	firewalldPath    = "/org/fedoraproject/FirewallD1"
	firewalldPolicy  = firewalldService + ".policy"
)

var errFirewalldInactive = errors.New("automatic port allowance requires firewalld to be enabled and active")

func (m *FirewallManager) reconcileFirewalld(ctx context.Context, ports FirewallPorts) error {
	if err := checkFirewalldService(ctx); err != nil {
		return err
	}
	// Keep the bus connection alive for rollback even if the operation is canceled.
	busCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(busCtx))
	if err != nil {
		return fmt.Errorf("%w: connect to system D-Bus: %v", errFirewalldInactive, err)
	}
	defer conn.Close()

	// Calling only the current unique owner prevents D-Bus from starting firewalld,
	// including if the administrator stops or restarts it during reconciliation.
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", dbus.FlagNoAutoStart, firewalldService).Store(&owner); err != nil {
		return fmt.Errorf("%w: query firewalld D-Bus owner: %v", errFirewalldInactive, err)
	}
	return reconcileFirewalldPolicy(ctx, conn.Object(owner, firewalldPath), m.config.Policy, m.owner, ports)
}

func checkFirewalldService(ctx context.Context) error {
	if _, err := os.Stat("/run/systemd/system"); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("detect systemd: %w", err)
	}
	for _, query := range []string{"is-enabled", "is-active"} {
		if err := exec.CommandContext(ctx, "systemctl", query, "--quiet", "firewalld.service").Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: systemctl %s firewalld.service: %v", errFirewalldInactive, query, err)
		}
	}
	return nil
}

type firewalldObject interface {
	CallWithContext(context.Context, string, dbus.Flags, ...any) *dbus.Call
}

func reconcileFirewalldPolicy(ctx context.Context, firewall firewalldObject, name, owner string, ports FirewallPorts) error {
	desired, err := firewalldRules(ports)
	if err != nil {
		return err
	}
	live, err := readFirewalldPolicy(ctx, firewall, name, owner)
	if err != nil || slices.Equal(live, desired) {
		return err
	}

	// firewalld validates each policy update, then processes removals before
	// additions. Add to a superset first to keep existing ports available.
	union := sortedFirewalldRules(append(slices.Clone(live), desired...))
	if !slices.Equal(live, union) {
		if err := setFirewalldRules(ctx, firewall, name, union); err != nil {
			return restoreFirewalldRules(ctx, firewall, name, live, err)
		}
	}
	if !slices.Equal(desired, union) {
		if err := setFirewalldRules(ctx, firewall, name, desired); err != nil {
			return restoreFirewalldRules(ctx, firewall, name, live, err)
		}
	}
	return nil
}

func readFirewalldPolicy(ctx context.Context, firewall firewalldObject, name, owner string) ([]string, error) {
	var state dbus.Variant
	if err := firewall.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", dbus.FlagNoAutoStart, firewalldService, "state").Store(&state); err != nil {
		return nil, firewalldPolicyError(err, "read firewalld state")
	}
	if state.Value() != "RUNNING" {
		return nil, errFirewalldInactive
	}
	var panicMode bool
	if err := firewall.CallWithContext(ctx, firewalldService+".queryPanicMode", dbus.FlagNoAutoStart).Store(&panicMode); err != nil {
		return nil, firewalldPolicyError(err, "read firewalld panic mode")
	}
	if panicMode {
		return nil, errors.New("automatic port allowance is unavailable while firewalld is in panic mode")
	}
	var settings map[string]dbus.Variant
	if err := firewall.CallWithContext(ctx, firewalldPolicy+".getPolicySettings", dbus.FlagNoAutoStart, name).Store(&settings); err != nil {
		return nil, firewalldPolicyError(err, "read firewalld policy %s", name)
	}
	rules, err := validateFirewalldPolicy(settings, owner)
	if err != nil {
		return nil, fmt.Errorf("firewalld policy %s: %w", name, err)
	}
	var active map[string]map[string][]string
	if err := firewall.CallWithContext(ctx, firewalldPolicy+".getActivePolicies", dbus.FlagNoAutoStart).Store(&active); err != nil {
		return nil, firewalldPolicyError(err, "read active firewalld policies")
	}
	if _, ok := active[name]; !ok {
		return nil, fmt.Errorf("firewalld policy %s has no active ingress zone", name)
	}
	return rules, nil
}

func validateFirewalldPolicy(settings map[string]dbus.Variant, owner string) ([]string, error) {
	description, _ := settings["description"].Value().(string)
	target, _ := settings["target"].Value().(string)
	priority, _ := settings["priority"].Value().(int32)
	ingress, _ := settings["ingress_zones"].Value().([]string)
	egress, _ := settings["egress_zones"].Value().([]string)
	if description != owner || target != "CONTINUE" || priority <= 0 ||
		len(ingress) == 0 || slices.Contains(ingress, "HOST") || !slices.Equal(egress, []string{"HOST"}) {
		return nil, fmt.Errorf("expected dedicated policy with description %q, positive priority, CONTINUE target and ingress zone to HOST", owner)
	}
	for key, field := range settings {
		switch key {
		case "description", "target", "priority", "ingress_zones", "egress_zones", "rich_rules", "version", "short":
			continue
		}
		if !emptyFirewalldSetting(field.Value()) {
			return nil, fmt.Errorf("dedicated policy contains an unmanaged %s setting", key)
		}
	}
	rules, _ := settings["rich_rules"].Value().([]string)
	if field, ok := settings["rich_rules"]; ok && field.Signature().String() != "as" {
		return nil, errors.New("unexpected rich-rule response from firewalld")
	}
	for _, rule := range rules {
		if !validFirewalldRule(rule) {
			return nil, errors.New("dedicated policy contains an unmanaged rich rule")
		}
	}
	return sortedFirewalldRules(slices.Clone(rules)), nil
}

func emptyFirewalldSetting(value any) bool {
	switch value := value.(type) {
	case bool:
		return !value
	case []string:
		return len(value) == 0
	case [][]any:
		return len(value) == 0
	default:
		return false
	}
}

func firewalldRules(ports FirewallPorts) ([]string, error) {
	var rules []string
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
				return nil, errors.New("firewalld port must be nonzero")
			}
			rules = append(rules, fmt.Sprintf(`rule port port="%d" protocol=%q accept`, port, protocol.name))
		}
		for _, endpoint := range protocol.endpoints {
			addr := endpoint.Address.Unmap()
			if !validFirewalldAddress(addr) || endpoint.Port == 0 {
				return nil, fmt.Errorf("invalid firewalld endpoint %s port %d", endpoint.Address, endpoint.Port)
			}
			if !slices.Contains(protocol.wildcard, endpoint.Port) {
				family := "ipv6"
				if addr.Is4() {
					family = "ipv4"
				}
				rules = append(rules, fmt.Sprintf(`rule family=%q destination address=%q port port="%d" protocol=%q accept`, family, addr.String(), endpoint.Port, protocol.name))
			}
		}
	}
	return sortedFirewalldRules(rules), nil
}

var firewalldRule = regexp.MustCompile(`^rule(?: family="(ipv4|ipv6)" destination address="([0-9A-Fa-f.:]+)")? port port="([0-9]+)" protocol="(tcp|udp)" accept$`)

func validFirewalldRule(rule string) bool {
	match := firewalldRule.FindStringSubmatch(rule)
	if match == nil {
		return false
	}
	port, err := strconv.ParseUint(match[3], 10, 16)
	if err != nil || port == 0 {
		return false
	}
	if match[1] == "" {
		return true
	}
	addr, err := netip.ParseAddr(match[2])
	return err == nil && validFirewalldAddress(addr) && addr.Is4() == (match[1] == "ipv4")
}

func validFirewalldAddress(addr netip.Addr) bool {
	return addr.IsValid() && !addr.IsUnspecified() && addr.Zone() == ""
}

func sortedFirewalldRules(rules []string) []string {
	slices.Sort(rules)
	return slices.Compact(rules)
}

func setFirewalldRules(ctx context.Context, firewall firewalldObject, name string, rules []string) error {
	settings := map[string]dbus.Variant{"rich_rules": dbus.MakeVariant(rules)}
	err := firewall.CallWithContext(ctx, firewalldPolicy+".setPolicySettings", dbus.FlagNoAutoStart, name, settings).Err
	if err != nil {
		return firewalldPolicyError(err, "apply firewalld policy %s", name)
	}
	return nil
}

func restoreFirewalldRules(ctx context.Context, firewall firewalldObject, name string, previous []string, applyErr error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := setFirewalldRules(ctx, firewall, name, previous); err != nil {
		return errors.Join(applyErr, fmt.Errorf("restore previous firewalld rules: %w", err))
	}
	return applyErr
}

func firewalldPolicyError(err error, format string, args ...any) error {
	var failure dbus.Error
	if errors.As(err, &failure) {
		switch failure.Name {
		case "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface":
			return fmt.Errorf("%w: installed firewalld does not support the policy API", errFirewalldInactive)
		case "org.freedesktop.DBus.Error.NameHasNoOwner", "org.freedesktop.DBus.Error.ServiceUnknown":
			return fmt.Errorf("%w: firewalld disconnected from D-Bus", errFirewalldInactive)
		}
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}
