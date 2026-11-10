package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const (
	nodeAuthBodyLimit   = 4096
	nodeAuthBodyTimeout = 10 * time.Second
)

func (h *ReportHandler) SetNodeTokenKey(key []byte) error {
	tokens, err := service.NewNodeCredentialService(h.db, key)
	if err == nil {
		h.nodeTokens = tokens
	}
	return err
}

func (h *ReportHandler) RegisterHTTP(w http.ResponseWriter, r *http.Request) {
	request := &nodegrpc.RegisterNodeRequest{}
	if !readNodeProtobuf(w, r, request) {
		return
	}
	reply, err := h.enrollHTTP(r, request)
	if err != nil {
		writeNodeHTTPError(w, err)
		return
	}
	writeNodeProtobuf(w, reply)
}

func (h *ReportHandler) enrollHTTP(r *http.Request, request *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
	id, err := nodegrpc.ParseRegistrationTokenID(request.GetToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired registration token")
	}
	ctx := r.Context()
	if endpoint, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		ctx = peer.NewContext(ctx, &peer.Peer{Addr: net.TCPAddrFromAddrPort(endpoint)})
	}
	ipv4, ipv6, err := h.registrationIPs(ctx, request)
	if err != nil {
		return nil, err
	}
	if ipv4 == "" && ipv6 == "" {
		return nil, status.Error(codes.FailedPrecondition, "could not detect a node IP address")
	}
	reply, err := h.nodeTokens.Enroll(ctx, uint(id), request.GetToken(), ipv4, ipv6)
	if err != nil {
		return nil, nodeAuthError(ctx, err, "invalid or expired registration token")
	}
	gost.DisconnectNode(uint(id))
	return reply, nil
}

func (h *ReportHandler) RefreshHTTP(w http.ResponseWriter, r *http.Request) {
	request := &nodegrpc.RefreshNodeRequest{}
	if !readNodeProtobuf(w, r, request) {
		return
	}
	if request.GetNodeId() == 0 || request.GetNodeId() > 1<<32-1 || request.GetRequestId() == "" || len(request.GetRequestId()) > 128 {
		writeNodeHTTPError(w, status.Error(codes.InvalidArgument, "invalid node refresh request"))
		return
	}
	if !validNodeToken(request.GetRefreshToken()) {
		writeNodeHTTPError(w, status.Error(codes.Unauthenticated, "invalid or expired refresh token"))
		return
	}
	reply, err := h.nodeTokens.Refresh(r.Context(), uint(request.GetNodeId()), request.GetRefreshToken(), request.GetRequestId())
	if err != nil {
		writeNodeHTTPError(w, nodeAuthError(r.Context(), err, "invalid or expired refresh token"))
		return
	}
	writeNodeProtobuf(w, reply)
}

func (h *ReportHandler) AuthenticateSession(r *http.Request) (*model.GostNode, error) {
	ids, credentials := r.Header.Values(nodegrpc.NodeIDMetadata), r.Header.Values("Authorization")
	if len(ids) != 1 || len(credentials) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing node credentials")
	}
	id, err := strconv.ParseUint(ids[0], 10, 32)
	scheme, token, _ := strings.Cut(credentials[0], " ")
	if err != nil || id == 0 || !strings.EqualFold(scheme, "Bearer") || !validNodeToken(token) {
		return nil, status.Error(codes.Unauthenticated, "invalid node credentials")
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectAuthTimeout)
	defer cancel()
	node, err := h.nodeTokens.AuthenticateAccess(ctx, uint(id), token)
	if err != nil {
		return nil, nodeAuthError(ctx, err, "invalid or expired access token")
	}
	return node, nil
}

func validNodeToken(token string) bool {
	const bytes = 32
	if len(token) != base64.RawURLEncoding.EncodedLen(bytes) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(decoded) == bytes
}

func readNodeProtobuf(w http.ResponseWriter, r *http.Request, request proto.Message) bool {
	w.Header().Set("Cache-Control", "no-store")
	// Bound rejected bodies too: net/http may drain them before writing the response.
	// Only enrollment and refresh use this deadline, never WebSocket sessions.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(nodeAuthBodyTimeout))
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != nodegrpc.ProtobufContentType {
		http.Error(w, "expected a Protobuf request", http.StatusUnsupportedMediaType)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nodeAuthBodyLimit))
	// A failed read must remain bounded while net/http drains or closes the body.
	if err == nil {
		_ = controller.SetReadDeadline(time.Time{})
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		http.Error(w, "request body timed out", http.StatusRequestTimeout)
		return false
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err != nil || proto.Unmarshal(data, request) != nil {
		writeNodeHTTPError(w, status.Error(codes.InvalidArgument, "invalid Protobuf request"))
		return false
	}
	return true
}

func writeNodeProtobuf(w http.ResponseWriter, message proto.Message) {
	data, err := proto.Marshal(message)
	if err != nil {
		writeNodeHTTPError(w, status.Error(codes.Internal, "could not encode response"))
		return
	}
	w.Header().Set("Content-Type", nodegrpc.ProtobufContentType)
	_, _ = w.Write(data)
}

func writeNodeHTTPError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	code := http.StatusInternalServerError
	switch status.Code(err) {
	case codes.Unauthenticated:
		code = http.StatusUnauthorized
		w.Header().Set("WWW-Authenticate", "Bearer")
	case codes.PermissionDenied:
		code = http.StatusForbidden
	case codes.InvalidArgument, codes.FailedPrecondition:
		code = http.StatusBadRequest
	case codes.ResourceExhausted:
		code = http.StatusTooManyRequests
	case codes.Unavailable:
		code = http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		code = http.StatusGatewayTimeout
	}
	http.Error(w, status.Convert(err).Message(), code)
}

func nodeAuthError(ctx context.Context, err error, rejected string) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return status.Error(codes.Unauthenticated, rejected)
	}
	return status.Error(codes.Internal, "node authentication failed")
}
