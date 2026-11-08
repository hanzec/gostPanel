package handler

import (
	"context"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConnectReportLimitSurvivesStreamReplacement(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	nodes := []model.GostNode{
		{Name: "first", Username: "first", Password: "first-secret"},
		{Name: "second", Username: "second", Password: "second-secret"},
	}
	if err := f.db.Create(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	client := startTestReportServer(t, f.db)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reports := startTestReportStream(t, ctx, client, nodes[0], nil)
	request := &nodegrpc.NodeIPsRequest{NodeId: uint64(nodes[0].ID), Ipv4: "192.0.2.44"}
	for range ipReportBurst {
		if _, err := reports.UpdateNodeIPs(ctx, request); err != nil {
			t.Fatalf("IP report within the burst: %v", err)
		}
	}
	if _, err := reports.UpdateNodeIPs(ctx, request); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("IP report after exhausting the burst: %v", err)
	}

	replacement := startTestReportStream(t, ctx, client, nodes[0], nil)
	if _, err := replacement.UpdateNodeIPs(ctx, request); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("IP report after replacing the stream: %v", err)
	}
	if _, err := replacement.Report(ctx, &nodegrpc.ReportRequest{
		NodeId: request.NodeId, Observation: &observer.ObserveRequest{},
	}); err != nil {
		t.Fatalf("traffic report with the IP bucket exhausted: %v", err)
	}

	other := startTestReportStream(t, ctx, client, nodes[1], nil)
	if _, err := other.UpdateNodeIPs(ctx, &nodegrpc.NodeIPsRequest{
		NodeId: uint64(nodes[1].ID), Ipv4: "192.0.2.45",
	}); err != nil {
		t.Fatalf("IP report from another node: %v", err)
	}
}

func TestTrafficReportBucketRefillsAtConfiguredRate(t *testing.T) {
	var bucket reportBucket
	now := time.Now()
	for range trafficReportBurst {
		if !bucket.take(now, trafficReportsPerSecond, trafficReportBurst) {
			t.Fatal("bucket rejected a report within the burst")
		}
	}
	interval := time.Second / trafficReportsPerSecond
	if bucket.take(now.Add(interval/2), trafficReportsPerSecond, trafficReportBurst) {
		t.Fatal("bucket refilled a token too early")
	}
	if !bucket.take(now.Add(interval), trafficReportsPerSecond, trafficReportBurst) {
		t.Fatal("bucket did not refill a token")
	}
	if bucket.take(now.Add(interval), trafficReportsPerSecond, trafficReportBurst) {
		t.Fatal("bucket refilled an extra token")
	}
}

func TestReportRateLimitsPruneIdleNodes(t *testing.T) {
	now := time.Now()
	active := &nodeReportBuckets{lastUsed: now}
	limits := reportRateLimits{nodes: map[uint]*nodeReportBuckets{
		1: {lastUsed: now.Add(-2 * reportBucketIdle)},
		2: active,
	}}
	if !limits.allow(2, nodegrpc.V1_UpdateNodeIPs_FullMethodName) {
		t.Fatal("active node was throttled")
	}
	if _, ok := limits.nodes[1]; ok {
		t.Fatal("idle node bucket was retained")
	}
	if limits.nodes[2] != active {
		t.Fatal("active node bucket was replaced")
	}
}
