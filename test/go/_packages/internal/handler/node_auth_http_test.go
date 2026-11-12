package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestNodeHTTPEnrollmentRetriesAfterDatabaseFailure(t *testing.T) {
	f := newNodeAuthFixture(t)
	node := model.GostNode{Name: "ipv6"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	token := f.issue(t, node.ID)
	payload, err := proto.Marshal(&nodegrpc.RegisterNodeRequest{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	try := func(peer string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, nodegrpc.RegisterPath, bytes.NewReader(payload))
		req.Header.Set("Content-Type", nodegrpc.ProtobufContentType)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		f.handler.RegisterHTTP(rec, req)
		if rec.Code != want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("registration response = %d, %q, want %d with no-store", rec.Code, rec.Body, want)
		}
		return rec
	}
	try("", http.StatusBadRequest)
	if err := f.db.Exec(`CREATE TRIGGER fail_enrollment BEFORE UPDATE OF access_token_hash ON nodes
		BEGIN SELECT RAISE(ABORT, 'enrollment storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	try("[2001:db8::8]:39110", http.StatusInternalServerError)
	if err := f.db.Exec("DROP TRIGGER fail_enrollment").Error; err != nil {
		t.Fatal(err)
	}
	reply := &nodegrpc.RegisterNodeReply{}
	if err := proto.Unmarshal(try("[2001:db8::8]:39110", http.StatusOK).Body.Bytes(), reply); err != nil || reply.GetNodeId() != uint64(node.ID) {
		t.Fatalf("retry enrollment: %v", err)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if node.IPv6 != "2001:db8::8" || node.IPv4 != "" || node.Address != node.IPv6 {
		t.Fatalf("IPv6 peer enrollment = %q, %q, %q", node.IPv4, node.IPv6, node.Address)
	}
}

func TestNodeHTTPAuthRejectsMalformedRequests(t *testing.T) {
	f := newNodeAuthFixture(t)
	for _, tc := range []struct {
		name        string
		contentType string
		body        []byte
		want        int
	}{
		{"content type", "application/json", []byte("{}"), http.StatusUnsupportedMediaType},
		{"large body", nodegrpc.ProtobufContentType, bytes.Repeat([]byte{'x'}, nodeAuthBodyLimit+1), http.StatusRequestEntityTooLarge},
		{"invalid protobuf", nodegrpc.ProtobufContentType, []byte{0xff}, http.StatusBadRequest},
		{"empty refresh", nodegrpc.ProtobufContentType, nil, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, nodegrpc.RefreshPath, bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()
			f.handler.RefreshHTTP(rec, req)
			if rec.Code != tc.want || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("refresh response = %d, %q, want %d with no-store", rec.Code, rec.Body, tc.want)
			}
		})
	}
	_, err := f.refresh(t.Context(), &nodegrpc.RefreshNodeRequest{NodeId: 1, RefreshToken: "malformed", RequestId: "retry"})
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	req := httptest.NewRequest(http.MethodGet, nodegrpc.SessionPath, nil)
	req.Header.Set(nodegrpc.NodeIDMetadata, "1")
	req.Header.Set("Authorization", "Basic invalid")
	if _, err := f.handler.AuthenticateSession(req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("non-Bearer auth = %v", err)
	}
	req.Header.Add("Authorization", "Bearer duplicate")
	if _, err := f.handler.AuthenticateSession(req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("duplicate Authorization headers = %v", err)
	}
	if err := f.handler.SetNodeTokenKey([]byte("invalid")); err == nil {
		t.Fatal("accepted an invalid node token key")
	}
}

func TestNodeHTTPRefreshRejectsExpiredTokenAndCorruptedReceipt(t *testing.T) {
	f := newNodeAuthFixture(t)
	node := model.GostNode{Name: "refresh expiry"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	reply, err := f.register(t.Context(), &nodegrpc.RegisterNodeRequest{Token: f.issue(t, node.ID)})
	if err != nil {
		t.Fatal(err)
	}
	request := &nodegrpc.RefreshNodeRequest{
		NodeId: reply.GetNodeId(), RefreshToken: reply.GetCredentials().GetRefreshToken(), RequestId: "expiry-test",
	}
	if err := f.db.Model(&node).Update("refresh_expires_at", time.Now().Add(-time.Second).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.refresh(t.Context(), request)
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	if err := f.db.Model(&node).Update("refresh_expires_at", time.Now().Add(time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TRIGGER fail_refresh BEFORE UPDATE OF refresh_receipt ON nodes
		BEGIN SELECT RAISE(ABORT, 'refresh storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.refresh(t.Context(), request)
	requireNodeHTTPStatus(t, err, http.StatusInternalServerError)
	if err := f.db.Exec("DROP TRIGGER fail_refresh").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.refresh(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&node).Update("refresh_receipt", []byte("corrupted")).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.refresh(t.Context(), request)
	requireNodeHTTPStatus(t, err, http.StatusInternalServerError)
	if err := f.nodes.RevokeCredentials(t.Context(), node.ID+1); !errors.Is(err, bizerrors.ErrNodeNotFound) {
		t.Fatalf("revoke absent node = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, nodegrpc.SessionPath, nil)
	req.Header.Set(nodegrpc.NodeIDMetadata, strconv.FormatUint(reply.GetNodeId(), 10))
	req.Header.Set("Authorization", "Bearer "+reply.GetCredentials().GetAccessToken())
	if _, err := f.handler.AuthenticateSession(req); status.Code(err) != codes.Canceled {
		t.Fatalf("canceled auth = %v", err)
	}
}

func TestNodeHTTPStatusMappings(t *testing.T) {
	for code, want := range map[codes.Code]int{
		codes.PermissionDenied:  http.StatusForbidden,
		codes.ResourceExhausted: http.StatusTooManyRequests,
		codes.Unavailable:       http.StatusServiceUnavailable,
		codes.DeadlineExceeded:  http.StatusGatewayTimeout,
	} {
		rec := httptest.NewRecorder()
		writeNodeHTTPError(rec, status.Error(code, "node session error"))
		if rec.Code != want || !strings.Contains(rec.Body.String(), "node session error") {
			t.Fatalf("mapping %s = HTTP %d, %q", code, rec.Code, rec.Body)
		}
	}
}
