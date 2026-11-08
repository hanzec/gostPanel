package utils

import (
	"testing"

	"yagp/internal/model"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGostStatusMapping(t *testing.T) {
	for _, test := range []struct {
		state  string
		rule   model.RuleStatus
		tunnel model.TunnelStatus
	}{
		{"ready", model.RuleStatusRunning, model.TunnelStatusRunning},
		{"running", model.RuleStatusRunning, model.TunnelStatusRunning},
		{"failed", model.RuleStatusError, model.TunnelStatusError},
		{"stopped", model.RuleStatusStopped, model.TunnelStatusStopped},
		{"unknown", model.RuleStatusStopped, model.TunnelStatusStopped},
	} {
		if got := GostStateToRuleStatus(test.state); got != test.rule {
			t.Errorf("rule %q = %q, want %q", test.state, got, test.rule)
		}
		if got := GostStateToTunnelStatus(test.state); got != test.tunnel {
			t.Errorf("tunnel %q = %q, want %q", test.state, got, test.tunnel)
		}
	}
}

func TestGostClientRequiresEnrollment(t *testing.T) {
	node := &model.GostNode{Address: "[::1]", Port: 39000, Username: "node", Password: "password"}
	if err := GetGostClient(node).HealthCheck(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("node without enrolled certificate returned %v", err)
	}
}
