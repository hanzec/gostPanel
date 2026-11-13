package nodebridge

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

const (
	firewalldTestPolicy = "yagp-test"
	firewalldTestOwner  = "yagp daemon firewall 42"
	firewalldRule443    = `rule port port="443" protocol="tcp" accept`
	firewalldRule8443   = `rule port port="8443" protocol="tcp" accept`
)

func TestFirewalldReconcileRuntimeRules(t *testing.T) {
	firewall := newFirewalldTestObject(t, firewalldRule443)
	ports := FirewallPorts{TCP: []uint16{8443, 8443}, UDPAddresses: []FirewallEndpoint{
		{Address: netip.MustParseAddr("2001:db8::20"), Port: 53},
		{Address: netip.MustParseAddr("::ffff:192.0.2.5"), Port: 53},
	}}
	const ipv4 = `rule family="ipv4" destination address="192.0.2.5" port port="53" protocol="udp" accept`
	const ipv6 = `rule family="ipv6" destination address="2001:db8::20" port port="53" protocol="udp" accept`
	reconcile := func() {
		t.Helper()
		if err := reconcileFirewalldPolicy(t.Context(), firewall, firewalldTestPolicy, firewalldTestOwner, ports); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	want := [][]string{{ipv4, ipv6, firewalldRule443, firewalldRule8443}, {ipv4, ipv6, firewalldRule8443}}
	if !reflect.DeepEqual(firewall.updates, want) {
		t.Fatalf("firewalld updates = %#v, want %#v", firewall.updates, want)
	}
	reconcile()
	if len(firewall.updates) != 2 {
		t.Fatalf("unchanged reconcile applied %v", firewall.updates[2:])
	}
	firewall.settings["rich_rules"] = dbus.MakeVariant([]string{ipv4, firewalldRule8443})
	reconcile()
	if got := firewall.updates[2:]; !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("drift recovery = %v, want %v", got, want[1:])
	}
}

func TestFirewalldRejectsInactiveOrUnownedPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*firewalldTestObject)
		want   string
	}{
		{"stopped", func(f *firewalldTestObject) { f.state = "FAILED" }, "enabled and active"},
		{"panic", func(f *firewalldTestObject) { f.panicMode = true }, "panic mode"},
		{"inactive policy", func(f *firewalldTestObject) { f.active = false }, "no active ingress zone"},
		{"unowned policy", func(f *firewalldTestObject) { f.settings["description"] = dbus.MakeVariant("administrator") }, "expected dedicated policy"},
		{"early policy", func(f *firewalldTestObject) { f.settings["priority"] = dbus.MakeVariant(int32(-1)) }, "positive priority"},
		{"broad target", func(f *firewalldTestObject) { f.settings["target"] = dbus.MakeVariant("ACCEPT") }, "CONTINUE target"},
		{"egress policy", func(f *firewalldTestObject) { f.settings["egress_zones"] = dbus.MakeVariant([]string{"public"}) }, "to HOST"},
		{"existing services", func(f *firewalldTestObject) { f.settings["services"] = dbus.MakeVariant([]string{"ssh"}) }, "unmanaged services setting"},
		{"existing rich rule", func(f *firewalldTestObject) {
			f.settings["rich_rules"] = dbus.MakeVariant([]string{`rule family="ipv4" source address="192.0.2.1" drop`})
		}, "unmanaged rich rule"},
		{"old firewalld", func(f *firewalldTestObject) {
			f.policyError = dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod"}
		}, "does not support the policy API"},
	} {
		t.Run(test.name, func(t *testing.T) {
			firewall := newFirewalldTestObject(t)
			test.change(firewall)
			err := reconcileFirewalldPolicy(t.Context(), firewall, firewalldTestPolicy, firewalldTestOwner, FirewallPorts{TCP: []uint16{8443}})
			if err == nil || !strings.Contains(err.Error(), test.want) || len(firewall.updates) != 0 {
				t.Fatalf("unexpected reconcile = %v, updates = %v", err, firewall.updates)
			}
			if test.name == "old firewalld" && !errors.Is(err, errFirewalldInactive) {
				t.Fatalf("unsupported backend was reported as available: %v", err)
			}
		})
	}
}

func TestFirewalldRestoresPreviousRulesAfterFailedApply(t *testing.T) {
	for _, failingUpdate := range []int{1, 2} {
		firewall := newFirewalldTestObject(t, firewalldRule443)
		ctx, cancel := context.WithCancel(t.Context())
		firewall.apply = func(update int) error {
			if update == failingUpdate {
				cancel()
				return context.Canceled
			}
			return nil
		}
		err := reconcileFirewalldPolicy(ctx, firewall, firewalldTestPolicy, firewalldTestOwner, FirewallPorts{TCP: []uint16{8443}})
		cancel()
		if !errors.Is(err, context.Canceled) || len(firewall.updates) != failingUpdate+1 ||
			!slices.Equal(firewall.updates[failingUpdate], []string{firewalldRule443}) {
			t.Fatalf("failure at update %d: error = %v, calls = %v", failingUpdate, err, firewall.updates)
		}
	}
}

func TestFirewalldRejectsInvalidDesiredRulesBeforeApplying(t *testing.T) {
	for _, ports := range []FirewallPorts{
		{TCP: []uint16{0}},
		{UDPAddresses: []FirewallEndpoint{{Port: 53}}},
		{TCPAddresses: []FirewallEndpoint{{Address: netip.MustParseAddr("::"), Port: 443}}},
	} {
		firewall := newFirewalldTestObject(t)
		if err := reconcileFirewalldPolicy(t.Context(), firewall, firewalldTestPolicy, firewalldTestOwner, ports); err == nil || len(firewall.updates) != 0 {
			t.Fatalf("invalid ports %+v: error = %v, updates = %v", ports, err, firewall.updates)
		}
	}
}

type firewalldTestObject struct {
	t           *testing.T
	state       string
	active      bool
	panicMode   bool
	policyError error
	settings    map[string]dbus.Variant
	updates     [][]string
	apply       func(int) error
}

func newFirewalldTestObject(t *testing.T, rules ...string) *firewalldTestObject {
	t.Helper()
	return &firewalldTestObject{
		t: t, state: "RUNNING", active: true,
		settings: map[string]dbus.Variant{
			"description":   dbus.MakeVariant(firewalldTestOwner),
			"priority":      dbus.MakeVariant(int32(32767)),
			"target":        dbus.MakeVariant("CONTINUE"),
			"ingress_zones": dbus.MakeVariant([]string{"public"}),
			"egress_zones":  dbus.MakeVariant([]string{"HOST"}),
			"rich_rules":    dbus.MakeVariant(rules),
			"services":      dbus.MakeVariant([]string{}),
			"masquerade":    dbus.MakeVariant(false),
		},
	}
}

func (f *firewalldTestObject) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	f.t.Helper()
	if flags != dbus.FlagNoAutoStart {
		f.t.Fatalf("firewalld call permits auto activation: %s", method)
	}
	if ctx.Err() != nil {
		return &dbus.Call{Err: ctx.Err()}
	}
	switch method {
	case "org.freedesktop.DBus.Properties.Get":
		return &dbus.Call{Body: []any{dbus.MakeVariant(f.state)}}
	case firewalldService + ".queryPanicMode":
		return &dbus.Call{Body: []any{f.panicMode}}
	case firewalldPolicy + ".getActivePolicies":
		active := map[string]map[string][]string{}
		if f.active {
			active[firewalldTestPolicy] = map[string][]string{"ingress_zones": {"public"}, "egress_zones": {"HOST"}}
		}
		return &dbus.Call{Body: []any{active}}
	case firewalldPolicy + ".getPolicySettings":
		return &dbus.Call{Body: []any{f.settings}, Err: f.policyError}
	case firewalldPolicy + ".setPolicySettings":
		if len(args) != 2 || args[0] != firewalldTestPolicy {
			f.t.Fatalf("attempt to write unrelated policy: %v", args)
		}
		settings := args[1].(map[string]dbus.Variant)
		if len(settings) != 1 || settings["rich_rules"].Signature().String() != "as" {
			f.t.Fatalf("update touched unrelated firewalld policy settings: %v", settings)
		}
		f.updates = append(f.updates, settings["rich_rules"].Value().([]string))
		f.settings["rich_rules"] = settings["rich_rules"]
		if f.apply != nil {
			return &dbus.Call{Err: f.apply(len(f.updates))}
		}
		return &dbus.Call{}
	default:
		f.t.Fatalf("unexpected firewalld method %s", method)
		return nil
	}
}
