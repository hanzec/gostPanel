package service

import (
	"context"
	"testing"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"
	"yagp/test/go/streamtest"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestTransparentRedirectErrorsIdentifyNodeRequirements(t *testing.T) {
	for _, tc := range []struct {
		code    codes.Code
		message string
		want    error
	}{
		{codes.Unimplemented, "RPC is unavailable on this side of the stream", bizerrors.ErrTunnelTransparentDaemon},
		{codes.Unimplemented, "method ConfigureTransparentRedirect not implemented", bizerrors.ErrTunnelTransparentDaemon},
		{codes.Unimplemented, "transparent redirect requires a Linux node with a configuration file", bizerrors.ErrTunnelTransparentPlatform},
		{codes.Unimplemented, "nftables is unavailable: install the nftables package on this node", bizerrors.ErrTunnelTransparentNft},
		{codes.Unimplemented, "other unsupported operation", bizerrors.ErrTunnelTransparentRedirect},
		{codes.FailedPrecondition, "RPC is unavailable on this side of the stream", bizerrors.ErrTunnelTransparentRedirect},
		{codes.FailedPrecondition, "read Docker bridge addresses", bizerrors.ErrTunnelTransparentRedirect},
		{codes.Unavailable, "node disconnected", bizerrors.ErrTunnelTransparentRedirect},
	} {
		t.Run(tc.message+"/"+tc.code.String(), func(t *testing.T) {
			requireServiceError(t, transparentRedirectError(status.Error(tc.code, tc.message)), tc.want)
		})
	}
}

func TestTransparentTunnelRetriesAfterUnavailableRedirectRPC(t *testing.T) {
	for _, action := range []nodegrpc.TransparentRedirectAction{
		nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_PREPARE,
		nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_APPLY,
	} {
		t.Run(action.String(), func(t *testing.T) {
			f := newOriginalTargetTestTunnel(t)
			dispatch := nodegrpc.NewDispatcher(f.entryNode,
				nodegrpc.V1_Execute_FullMethodName, nodegrpc.V1_GetObserverEndpoint_FullMethodName,
				nodegrpc.V1_SyncTrafficQuota_FullMethodName, nodegrpc.V1_ConfigureTransparentRedirect_FullMethodName)
			session := streamtest.Open(t, func(ctx context.Context, method string, body []byte) ([]byte, error) {
				if method == nodegrpc.V1_ConfigureTransparentRedirect_FullMethodName {
					var request nodegrpc.TransparentRedirectRequest
					if err := proto.Unmarshal(body, &request); err != nil {
						return nil, err
					}
					if request.GetAction() == action {
						return nil, status.Error(codes.Unimplemented, "RPC is unavailable on this side of the stream")
					}
				}
				return dispatch(ctx, method, body)
			})
			t.Cleanup(gost.RegisterNodeStream(f.entry.ID, f.entry.SessionGeneration, f.entry.Username, f.entry.Password, session))
			requireServiceError(t, f.start(), bizerrors.ErrTunnelTransparentDaemon)
			f.assertStopped(t)
			entrySaves, entrySavedService := f.entryNode.savedService()
			exitSaves, exitSavedService := f.exitNode.savedService()
			if entrySavedService || exitSavedService {
				t.Fatal("failed redirect left a persisted running listener")
			}
			if action == nodegrpc.TransparentRedirectAction_TRANSPARENT_REDIRECT_ACTION_PREPARE && (entrySaves != 0 || exitSaves != 0) {
				t.Fatal("missing preparation RPC changed persisted Gost configuration")
			}

			upgraded := streamtest.Open(t, dispatch)
			t.Cleanup(gost.RegisterNodeStream(f.entry.ID, f.entry.SessionGeneration, f.entry.Username, f.entry.Password, upgraded))
			requireServiceError(t, f.start(), nil)
			current, err := f.service.GetByID(f.tunnel.ID)
			requireServiceError(t, err, nil)
			active, _, _ := f.entryNode.redirectStatus()
			if current.Status != model.TunnelStatusRunning || !active {
				t.Fatalf("upgraded entry did not bring the transparent tunnel online: status=%v, redirect active=%v", current.Status, active)
			}
			requireServiceError(t, f.stop(), nil)
			f.assertStopped(t)
		})
	}
}
