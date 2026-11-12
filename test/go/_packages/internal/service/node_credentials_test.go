package service

import (
	"bytes"
	"testing"
	"time"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestNodeCredentialRefreshSurvivesLostResponse(t *testing.T) {
	ctx := t.Context()
	db := newServiceTestDB(t)
	node := &model.GostNode{Name: "refresh"}
	saveServiceFixtures(t, db, node)
	nodes := NewNodeService(db, "")
	key := bytes.Repeat([]byte{0x7c}, 32)
	if _, err := NewNodeCredentialService(db, key[:16]); err == nil {
		t.Fatal("accepted an invalid receipt key")
	}
	tokens, err := NewNodeCredentialService(db, key)
	requireServiceError(t, err, nil)
	registration, err := nodes.IssueRegistrationToken(ctx, node.ID, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	enrolled, err := tokens.Enroll(ctx, node.ID, registration.Token, testIP, "")
	requireServiceError(t, err, nil)
	old := enrolled.GetCredentials()

	if err := db.Exec(`CREATE TRIGGER fail_refresh BEFORE UPDATE OF refresh_token_hash ON nodes
		BEGIN SELECT RAISE(ABORT, 'storage unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	const requestID = "refresh-retry-id"
	if _, err := tokens.Refresh(ctx, node.ID, old.GetRefreshToken(), requestID); err == nil {
		t.Fatal("refresh reported success when the database rejected rotation")
	}
	requireServiceError(t, db.Exec("DROP TRIGGER fail_refresh").Error, nil)

	fresh, err := tokens.Refresh(ctx, node.ID, old.GetRefreshToken(), requestID)
	requireServiceError(t, err, nil)
	if fresh.GetAccessToken() == old.GetAccessToken() || fresh.GetRefreshToken() == old.GetRefreshToken() {
		t.Fatal("refresh must rotate both credentials")
	}
	_, err = tokens.AuthenticateAccess(ctx, node.ID, old.GetAccessToken())
	requireServiceError(t, err, gorm.ErrRecordNotFound)
	authenticated, err := tokens.AuthenticateAccess(ctx, node.ID, fresh.GetAccessToken())
	requireServiceError(t, err, nil)
	if authenticated.SessionGeneration != 1 {
		t.Fatal("rotation changed the active session's enrollment generation")
	}

	// Retry after a lost response and a restart, using the same key and request ID.
	restarted, err := NewNodeCredentialService(db, key)
	requireServiceError(t, err, nil)
	replayed, err := restarted.Refresh(ctx, node.ID, old.GetRefreshToken(), requestID)
	requireServiceError(t, err, nil)
	if !proto.Equal(fresh, replayed) {
		t.Fatal("idempotent retry changed the committed credentials")
	}
	_, err = restarted.Refresh(ctx, node.ID, old.GetRefreshToken(), "wrong-request-id")
	requireServiceError(t, err, gorm.ErrRecordNotFound)
	otherKey, err := NewNodeCredentialService(db, bytes.Repeat([]byte{0x3c}, 32))
	requireServiceError(t, err, nil)
	if _, err := otherKey.Refresh(ctx, node.ID, old.GetRefreshToken(), requestID); err == nil {
		t.Fatal("a different key decrypted the refresh receipt")
	}
	requireServiceError(t, db.Model(node).Update("refresh_retry_until", time.Now().Add(-time.Second).Unix()).Error, nil)
	_, err = restarted.Refresh(ctx, node.ID, old.GetRefreshToken(), requestID)
	requireServiceError(t, err, gorm.ErrRecordNotFound)

	requireServiceError(t, nodes.RevokeCredentials(ctx, node.ID+1), bizerrors.ErrNodeNotFound)
	requireServiceError(t, db.Exec(`CREATE TRIGGER fail_revoke BEFORE UPDATE OF session_generation ON nodes
		BEGIN SELECT RAISE(ABORT, 'revocation storage unavailable'); END`).Error, nil)
	if err := nodes.RevokeCredentials(ctx, node.ID); err == nil {
		t.Fatal("revocation reported success when storage rejected it")
	}
	_, err = restarted.AuthenticateAccess(ctx, node.ID, fresh.GetAccessToken())
	requireServiceError(t, err, nil)
	requireServiceError(t, db.Exec("DROP TRIGGER fail_revoke").Error, nil)
	requireServiceError(t, nodes.RevokeCredentials(ctx, node.ID), nil)
	_, err = restarted.AuthenticateAccess(ctx, node.ID, fresh.GetAccessToken())
	requireServiceError(t, err, gorm.ErrRecordNotFound)
	_, err = restarted.Refresh(ctx, node.ID, fresh.GetRefreshToken(), "revoked-credential")
	requireServiceError(t, err, gorm.ErrRecordNotFound)
}
