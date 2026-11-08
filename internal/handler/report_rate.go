package handler

import (
	"sync"
	"time"

	"yagp/pkg/nodegrpc"
)

const (
	// Gost observes each rule on a 5-second period. Leave room for aligned reports.
	trafficReportsPerSecond = 100
	trafficReportBurst      = 500
	ipReportsPerSecond      = 1
	ipReportBurst           = 5
	reportBucketIdle        = time.Minute
)

// Buckets are keyed by authenticated node ID so replacing a stream cannot reset them.
type reportRateLimits struct {
	mu        sync.Mutex
	nodes     map[uint]*nodeReportBuckets
	cleanupAt time.Time
}

type nodeReportBuckets struct {
	traffic  reportBucket
	ip       reportBucket
	lastUsed time.Time
}

type reportBucket struct {
	tokens    float64
	updatedAt time.Time
}

func (l *reportRateLimits) allow(id uint, method string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !now.Before(l.cleanupAt) {
		for id, node := range l.nodes {
			if now.Sub(node.lastUsed) >= reportBucketIdle {
				delete(l.nodes, id)
			}
		}
		l.cleanupAt = now.Add(reportBucketIdle)
	}
	if l.nodes == nil {
		l.nodes = make(map[uint]*nodeReportBuckets)
	}
	node := l.nodes[id]
	if node == nil {
		node = &nodeReportBuckets{}
		l.nodes[id] = node
	}
	node.lastUsed = now
	if method == nodegrpc.V1_Report_FullMethodName {
		return node.traffic.take(now, trafficReportsPerSecond, trafficReportBurst)
	}
	return node.ip.take(now, ipReportsPerSecond, ipReportBurst)
}

func (b *reportBucket) take(now time.Time, perSecond, burst float64) bool {
	if b.updatedAt.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+now.Sub(b.updatedAt).Seconds()*perSecond)
	}
	b.updatedAt = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
