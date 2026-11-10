package handler

import (
	"context"
	"math"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReportRejectsCumulativeOverflowAndRollsBackBatch(t *testing.T) {
	for _, test := range []struct {
		name, table, column        string
		input, output, connections uint64
	}{
		{"rule input", "rules", "input_bytes", 1, 0, 0},
		{"rule output", "rules", "output_bytes", 0, 1, 0},
		{"rule total", "rules", "total_bytes", 1, 1, 0},
		{"rule requests", "rules", "total_requests", 0, 0, 1},
		{"node input", "nodes", "input_bytes", 1, 0, 0},
		{"node output", "nodes", "output_bytes", 0, 1, 0},
		{"node total", "nodes", "total_bytes", 1, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newHandlerHTTPFixture(t)
			node := model.GostNode{Name: "reporter", Username: "node", Password: "secret"}
			if err := f.db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			rules := []model.GostRule{
				{Name: "earlier event", Type: model.RuleTypeForward, NodeID: &node.ID, ListenPort: 12001},
				{Name: "overflow event", Type: model.RuleTypeForward, NodeID: &node.ID, ListenPort: 12002},
			}
			if err := f.db.Create(&rules).Error; err != nil {
				t.Fatal(err)
			}
			id := rules[1].ID
			if test.table == "nodes" {
				id = node.ID
			}
			if err := f.db.Table(test.table).Where("id = ?", id).UpdateColumn(test.column, int64(math.MaxInt64)).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			reports := startTestReportStream(t, ctx, startTestReportServer(t, f.db), node, nil)
			req := &nodegrpc.ReportRequest{NodeId: uint64(node.ID), Observation: &observer.ObserveRequest{Events: []*observer.Event{
				trafficEvent(rules[0].ID, 0, 0, 1),
				trafficEvent(rules[1].ID, test.input, test.output, test.connections),
			}}}
			if _, err := reports.Report(ctx, req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("overflow report = %v", err)
			}
			for _, original := range rules {
				var stored model.GostRule
				if err := f.db.First(&stored, original.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.ObserverCounters != (model.ObserverCounters{}) {
					t.Fatalf("rejected batch advanced checkpoint: %+v", stored.ObserverCounters)
				}
				if original.ID == rules[0].ID && stored.TotalRequests != 0 {
					t.Fatal("rejected batch committed its earlier event")
				}
			}
			var storedNode model.GostNode
			if err := f.db.First(&storedNode, node.ID).Error; err != nil {
				t.Fatalf("node was corrupted: %v", err)
			}
			var value int64
			if err := f.db.Table(test.table).Select(test.column).Where("id = ?", id).Scan(&value).Error; err != nil || value != math.MaxInt64 {
				t.Fatalf("counter changed: %d, %v", value, err)
			}
		})
	}
}

func TestReportRejectsOverflowAfterCounterRestart(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "reporter", Username: "node", Password: "secret"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.GostRule{Name: "forward", Type: model.RuleTypeForward, NodeID: &node.ID, ListenPort: 12001}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reports := startTestReportStream(t, ctx, startTestReportServer(t, f.db), node, nil)
	req := &nodegrpc.ReportRequest{NodeId: uint64(node.ID), Observation: &observer.ObserveRequest{Events: []*observer.Event{trafficEvent(rule.ID, math.MaxInt64, 0, 0)}}}
	if _, err := reports.Report(ctx, req); err != nil {
		t.Fatalf("exact maximum should be accepted: %v", err)
	}
	// Repeated snapshots remain valid; a restarted counter would add one more byte.
	if _, err := reports.Report(ctx, req); err != nil {
		t.Fatalf("unchanged maximum should be accepted: %v", err)
	}
	req.Observation.Events = []*observer.Event{trafficEvent(rule.ID, 1, 0, 0)}
	if _, err := reports.Report(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("counter restart overflow = %v", err)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil || node.TotalBytes != math.MaxInt64 {
		t.Fatalf("node after rejected overflow: %d, %v", node.TotalBytes, err)
	}
	if err := f.db.First(&rule, rule.ID).Error; err != nil || rule.TotalBytes != math.MaxInt64 || rule.ObserverCounters.InputBytes != math.MaxInt64 {
		t.Fatalf("rule after rejected overflow: %+v, %v", rule, err)
	}
}

func TestReportContinuesNormallyAfterRejectedOverflow(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "reporter", Username: "node", Password: "secret"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.GostRule{Name: "forward", Type: model.RuleTypeForward, NodeID: &node.ID, ListenPort: 12001}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reports := startTestReportStream(t, ctx, startTestReportServer(t, f.db), node, nil)
	request := &nodegrpc.ReportRequest{NodeId: uint64(node.ID), Observation: &observer.ObserveRequest{}}
	for _, input := range []uint64{10, 1} {
		request.Observation.Events = []*observer.Event{trafficEvent(rule.ID, input, 0, input)}
		if _, err := reports.Report(ctx, request); err != nil {
			t.Fatalf("normal report or counter restart was rejected: %v", err)
		}
	}
	request.Observation.Events = []*observer.Event{trafficEvent(rule.ID, math.MaxInt64, 0, math.MaxInt64)}
	if _, err := reports.Report(ctx, request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("cumulative overflow report = %v", err)
	}
	// The rejected snapshot must not become the baseline for the next genuine report.
	request.Observation.Events = []*observer.Event{trafficEvent(rule.ID, 3, 0, 3)}
	for range 2 {
		if _, err := reports.Report(ctx, request); err != nil {
			t.Fatalf("normal report after overflow was rejected: %v", err)
		}
	}
	if err := f.db.First(&rule, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rule.InputBytes != 13 || rule.OutputBytes != 0 || rule.TotalBytes != 13 || rule.TotalRequests != 13 || rule.ObserverCounters != (model.ObserverCounters{InputBytes: 3, Connections: 3}) {
		t.Fatalf("recovery lost or double-counted rule traffic: %+v", rule)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.InputBytes != 13 || node.OutputBytes != 0 || node.TotalBytes != 13 {
		t.Fatalf("recovery lost or double-counted node traffic: %+v", node)
	}
}
