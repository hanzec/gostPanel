package nodebridge

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/types/known/emptypb"
)

func controlDispatcher(control *controlServer) nodegrpc.StreamHandler {
	return nodegrpc.NewDispatcher(control,
		nodegrpc.V1_Execute_FullMethodName,
		nodegrpc.V1_CheckListener_FullMethodName,
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

func maintainCredentials(ctx context.Context, credentials *credentialManager) {
	for delay := credentials.nextRefresh(); ctx.Err() == nil; {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if _, err := credentials.accessToken(ctx, refreshLeadTime); err != nil {
			if ctx.Err() == nil {
				log.Printf("could not refresh panel credentials: %v", err)
			}
			delay = 30 * time.Second
			continue
		}
		delay = credentials.nextRefresh()
	}
}

func maintainPanelChannel(ctx context.Context, credentials *credentialManager, configPath string, current *atomic.Pointer[nodegrpc.Session], handler nodegrpc.StreamHandler) {
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 30*time.Second) {
		connected, err := runPanelSession(ctx, credentials, configPath, current, handler)
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

func runPanelSession(ctx context.Context, credentials *credentialManager, configPath string, current *atomic.Pointer[nodegrpc.Session], handler nodegrpc.StreamHandler) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	accessToken, err := credentials.accessToken(ctx, 0)
	if err != nil {
		return false, err
	}
	stream, err := credentials.host.DialSession(ctx, credentials.client, credentials.nodeID, accessToken)
	if err != nil {
		return false, err
	}
	defer stream.Close()

	session := nodegrpc.NewSession(stream, handler)
	defer session.Close()
	client := nodegrpc.NewV1Client(session)
	done := make(chan error, 1)
	go func() { done <- session.Run() }()
	if err := fetchPanelConfig(ctx, client, configPath); err != nil {
		return false, fmt.Errorf("load panel configuration: %w", err)
	}
	current.Store(session)
	defer current.Store(nil)
	ipTicks := time.NewTicker(5 * time.Minute)
	defer ipTicks.Stop()
	go reportPublicIPChanges(ctx, credentials.nodeID, client, DetectPublicIPs, ipTicks.C)
	return true, <-done
}

func fetchPanelConfig(ctx context.Context, client nodegrpc.V1Client, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	config, err := client.GetNodeConfig(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	return storePanelConfig(path, config)
}
