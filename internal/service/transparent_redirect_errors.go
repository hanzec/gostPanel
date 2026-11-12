package service

import (
	"yagp/internal/errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func transparentRedirectError(err error) error {
	if status.Code(err) == codes.Unimplemented {
		// Older stream dispatchers and unsupported node environments use the same code.
		switch status.Convert(err).Message() {
		case "RPC is unavailable on this side of the stream", "method ConfigureTransparentRedirect not implemented":
			return errors.ErrTunnelTransparentDaemon
		case "transparent redirect requires a Linux node with a configuration file":
			return errors.ErrTunnelTransparentPlatform
		case "nftables is unavailable: install the nftables package on this node":
			return errors.ErrTunnelTransparentNft
		}
	}
	return errors.ErrTunnelTransparentRedirect
}
