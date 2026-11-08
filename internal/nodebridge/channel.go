package nodebridge

import (
	"context"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func controlDispatcher(control *controlServer) nodegrpc.StreamHandler {
	return nodegrpc.NewDispatcher(control,
		nodegrpc.V1_Execute_FullMethodName,
		nodegrpc.V1_GetObserverEndpoint_FullMethodName,
		nodegrpc.V1_GetGostServiceStatus_FullMethodName,
		nodegrpc.V1_RestartGostService_FullMethodName,
		nodegrpc.V1_GetGostLogs_FullMethodName,
		nodegrpc.V1_GetNftConfig_FullMethodName,
		nodegrpc.V1_ApplyNftConfig_FullMethodName,
		nodegrpc.V1_SyncTrafficQuota_FullMethodName,
		nodegrpc.V1_GetUpgradeStatus_FullMethodName,
		nodegrpc.V1_StartUpgrade_FullMethodName,
	)
}

func maintainPanelChannel(ctx context.Context, nodeID uint64, panel nodegrpc.V1Client, current *atomic.Pointer[nodegrpc.Session], handler nodegrpc.StreamHandler) {
	ctx = metadata.AppendToOutgoingContext(ctx, nodegrpc.NodeIDMetadata, strconv.FormatUint(nodeID, 10))
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 30*time.Second) {
		connected, err := runPanelSession(ctx, nodeID, panel, current, handler)
		if ctx.Err() != nil {
			return
		}
		log.Printf("panel stream disconnected: %v", err)
		if connected {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func runPanelSession(ctx context.Context, nodeID uint64, panel nodegrpc.V1Client, current *atomic.Pointer[nodegrpc.Session], handler nodegrpc.StreamHandler) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Limit the handshake without imposing a deadline on the established stream.
	handshake := time.AfterFunc(15*time.Second, cancel)
	defer handshake.Stop()
	stream, err := panel.Connect(ctx)
	if err != nil {
		return false, err
	}
	headers, err := stream.Header()
	if err != nil {
		return false, err
	}
	if ready := headers.Get(nodegrpc.ReadyMetadata); len(ready) != 1 || ready[0] != "1" {
		if _, err := stream.Recv(); err != nil {
			return false, err
		}
		return false, status.Error(codes.Unavailable, "panel did not acknowledge node stream")
	}
	handshake.Stop()

	session := nodegrpc.NewSession(stream, handler)
	current.Store(session)
	defer current.Store(nil)
	ipTicks := time.NewTicker(5 * time.Minute)
	defer ipTicks.Stop()
	go reportPublicIPChanges(ctx, nodeID, nodegrpc.NewV1Client(session), DetectPublicIPs, ipTicks.C)
	return true, session.Run()
}
