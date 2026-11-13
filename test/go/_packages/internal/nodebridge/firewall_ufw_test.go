package nodebridge

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const ufwTestOwner = "yagp daemon firewall 42"
const ufwTestV4 = `-P INPUT DROP
-N ufw-before-input
-N ufw-user-input
-A INPUT -j ufw-before-input
-A ufw-before-input -j ufw-user-input
`
const ufwTestV6 = `-P INPUT DROP
-N ufw6-before-input
-N ufw6-user-input
-A INPUT -j ufw6-before-input
-A ufw6-before-input -j ufw6-user-input
`

func TestUFWReconcileActiveAndPreservesHostPolicy(t *testing.T) {
	const manualSSH = `-A ufw-user-input -p tcp -m tcp --dport 22 -j ACCEPT`
	const ownedTCP = `-A ufw-user-input -p tcp -m tcp --dport 8443 -m comment --comment "yagp daemon firewall 42" -j ACCEPT`
	const manualDeny = `-A ufw-user-input -p tcp -m tcp --dport 8443 -j DROP`
	const scopedDeny = `-A ufw-user-input -s 192.0.2.1/32 -p udp -m udp --dport 8444 -j DROP`
	const ownedUDP = `-A ufw-user-input -p udp -m udp --dport 8444 -m comment --comment "yagp daemon firewall 42" -j ACCEPT`
	var transactions []string
	backend := ufwBackend{
		readFile: ufwTestFiles(true, false), systemd: true,
		run: func(_ context.Context, executable string, batch []byte, args ...string) ([]byte, error) {
			switch executable {
			case "systemctl":
				if !reflect.DeepEqual(args, []string{"is-enabled", "ufw.service"}) {
					return nil, fmt.Errorf("unexpected systemctl call: %q", args)
				}
				return []byte("enabled\n"), nil
			case "ufw":
				if !reflect.DeepEqual(args, []string{"status"}) {
					return nil, fmt.Errorf("unexpected UFW call: %q", args)
				}
				return []byte("Status: active\n"), nil
			case "iptables":
				return []byte(ufwTestV4 + strings.Join([]string{manualSSH, ownedTCP, ownedUDP, manualDeny, scopedDeny}, "\n")), nil
			case "iptables-restore":
				transactions = append(transactions, strings.Join(args, " ")+"\n"+string(batch))
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected command %s", executable)
			}
		},
	}
	ports := FirewallPorts{TCP: []uint16{22, 8443}, UDP: []uint16{8444}}
	if err := backend.reconcile(t.Context(), "ufw", ufwTestOwner, ports); err != nil {
		t.Fatal(err)
	}
	want := "*filter\n-D" + ownedTCP[2:] + "\n-D" + ownedUDP[2:] + "\n" + ownedUDP + "\nCOMMIT\n"
	if !slices.Equal(transactions, []string{"--wait 2 --noflush --test\n" + want, "--wait 2 --noflush\n" + want}) {
		t.Fatalf("unexpected UFW transactions: %q", transactions)
	}
}

func TestUFWInactiveAndDisabledNeverApplyRules(t *testing.T) {
	for _, test := range []struct {
		name, output string
		enabled      bool
		systemd      bool
		wantCalls    int
	}{
		{name: "not enabled"},
		{name: "systemd unit disabled", enabled: true, systemd: true, output: "disabled\n", wantCalls: 1},
		{name: "kernel inactive", enabled: true, output: "Status: inactive\n", wantCalls: 1},
		{name: "missing kernel jump", enabled: true, output: "Status: active\n", wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			backend := ufwBackend{
				readFile: ufwTestFiles(test.enabled, false), systemd: test.systemd,
				run: func(_ context.Context, executable string, batch []byte, args ...string) ([]byte, error) {
					calls++
					if len(batch) != 0 || executable == "ufw" && !reflect.DeepEqual(args, []string{"status"}) {
						t.Fatalf("unexpected firewall mutation: %q %q %s", executable, args, batch)
					}
					return []byte(test.output), nil
				},
			}
			err := backend.reconcile(t.Context(), "ufw", ufwTestOwner, FirewallPorts{TCP: []uint16{8443}})
			if !errors.Is(err, errUFWInactive) || calls != test.wantCalls {
				t.Fatalf("inactive UFW reconcile = %v, calls = %d, want %d", err, calls, test.wantCalls)
			}
		})
	}
}

func TestUFWBatchFailurePreservesPriorRules(t *testing.T) {
	for _, stage := range []string{"check", "apply"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls []string
			const oldV4 = `-A ufw-user-input -p tcp -m tcp --dport 10001 -m comment --comment "yagp daemon firewall 42" -j ACCEPT`
			backend := ufwBackend{
				readFile: ufwTestFiles(true, true), ipv6: true,
				run: func(ctx context.Context, executable string, batch []byte, args ...string) ([]byte, error) {
					switch executable {
					case "ufw":
						return []byte("Status: active\n"), nil
					case "iptables":
						return []byte(ufwTestV4 + oldV4), nil
					case "ip6tables":
						return []byte(ufwTestV6), nil
					}
					commandStage := "apply"
					if slices.Contains(args, "--test") {
						commandStage = "check"
					}
					if executable == "ip6tables-restore" && commandStage == stage {
						calls = append(calls, executable+" "+commandStage)
						cancel()
						return nil, context.Canceled
					}
					if strings.Contains(string(batch), "-I ufw-user-input 1 ") {
						if ctx.Err() != nil || !strings.Contains(string(batch), "--dport 10001 ") {
							t.Fatalf("invalid rollback %q, error %v", batch, ctx.Err())
						}
						commandStage = "rollback"
					}
					calls = append(calls, executable+" "+commandStage)
					return nil, nil
				},
			}
			err := backend.reconcile(ctx, "ufw", ufwTestOwner, FirewallPorts{TCP: []uint16{10002}})
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), stage+" UFW rules") {
				t.Fatalf("%s failure: %v", stage, err)
			}
			want := []string{"iptables-restore check", "ip6tables-restore check"}
			if stage == "apply" {
				want = append(want, "iptables-restore apply", "ip6tables-restore apply", "iptables-restore rollback")
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
		})
	}
}

func TestUFWReportsDisabledIPv6AndReconcilesIPv4(t *testing.T) {
	var changes int
	backend := ufwBackend{
		readFile: ufwTestFiles(true, false), ipv6: true,
		run: func(_ context.Context, executable string, batch []byte, _ ...string) ([]byte, error) {
			switch executable {
			case "ufw":
				return []byte("Status: active\n"), nil
			case "iptables":
				return []byte(ufwTestV4), nil
			case "iptables-restore":
				if !strings.Contains(string(batch), `--dport 20001 -m comment --comment "yagp daemon firewall 42" -j ACCEPT`) {
					t.Fatalf("IPv4 update omitted: %s", batch)
				}
				changes++
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected command %s", executable)
			}
		},
	}
	ports := FirewallPorts{
		TCP:          []uint16{20001},
		UDPAddresses: []FirewallEndpoint{{Address: netip.MustParseAddr("2001:db8::2"), Port: 20002}},
	}
	err := backend.reconcile(t.Context(), "ufw", ufwTestOwner, ports)
	if err == nil || !strings.Contains(err.Error(), "UFW IPv6 support is disabled for listener [2001:db8::2]:20002") || changes != 2 {
		t.Fatalf("IPv4 changes = %d, error = %v", changes, err)
	}
}

func TestUFWReportsUnrecognizedOwnedRule(t *testing.T) {
	const changed = `-A ufw-user-input -s 192.0.2.1/32 -p udp -m udp --dport 8444 -m comment --comment "yagp daemon firewall 42" -j ACCEPT`
	backend := ufwBackend{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		return []byte(ufwTestV4 + changed), nil
	}}
	plan, err := backend.plan(t.Context(), "iptables", "ufw-user-input", ufwTestOwner, nil)
	if len(plan.batch) != 0 || err == nil || !strings.Contains(err.Error(), "unrecognized rule marked") {
		t.Fatalf("manually changed UFW rule: plan = %s, err = %v", plan.batch, err)
	}
}

func ufwTestFiles(enabled, ipv6 bool) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		setting := "no"
		switch path {
		case "/etc/ufw/ufw.conf":
			if enabled {
				setting = "yes"
			}
			return []byte("ENABLED=" + setting + "\n"), nil
		case "/etc/default/ufw":
			if ipv6 {
				setting = "yes"
			}
			return []byte("IPV6=" + setting + "\n"), nil
		default:
			return nil, fmt.Errorf("unexpected file %s", path)
		}
	}
}
