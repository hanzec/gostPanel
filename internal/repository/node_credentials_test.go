package repository

import (
	"errors"
	"testing"
	"time"

	"yagp/internal/model"

	"gorm.io/gorm"
)

func TestNodeRepositoryRotatesAndRevokesCredentials(t *testing.T) {
	nodes := NewNodeRepository(openRepositoryDB(t))
	ctx := t.Context()
	const now int64 = 1000
	registrationExpires := time.Unix(now+100, 0)
	node := model.GostNode{
		Name: "edge", Username: "edge", Password: "local-only",
		RegistrationTokenHash: "registration", RegistrationExpiresAt: &registrationExpires,
		NodeCredentials: model.NodeCredentials{
			SessionGeneration: 4,
			AccessTokenHash:   "access", AccessExpiresAt: now + 10,
			RefreshTokenHash: "refresh", RefreshExpiresAt: now + 100,
		},
	}
	mustPersist(t, nodes.Create(&node))
	current, err := nodes.FindNodeForAccess(ctx, node.ID, node.AccessTokenHash, now)
	mustPersist(t, err)
	if current.ID != node.ID || current.SessionGeneration != node.SessionGeneration {
		t.Fatal("access lookup did not return the enrolled node")
	}
	for _, attempt := range []struct {
		name string
		id   uint
		hash string
		at   int64
	}{
		{"wrong node", node.ID + 1, node.AccessTokenHash, now},
		{"wrong token", node.ID, "different-access", now},
		{"expired token", node.ID, node.AccessTokenHash, node.AccessExpiresAt},
	} {
		if _, err := nodes.FindNodeForAccess(ctx, attempt.id, attempt.hash, attempt.at); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("access lookup with %s: %v", attempt.name, err)
		}
	}

	next := model.NodeCredentials{
		AccessTokenHash: "new-access", AccessExpiresAt: now + 20,
		RefreshTokenHash: "new-refresh", RefreshExpiresAt: now + 200,
		PreviousRefreshTokenHash: node.RefreshTokenHash, RefreshRequestHash: "request",
		RefreshReceipt: []byte("encrypted reply"), RefreshRetryUntil: now + 5,
	}
	rotated, err := nodes.RotateNodeCredentials(ctx, node.ID, node.RefreshTokenHash, node.RefreshExpiresAt, next)
	mustPersist(t, err)
	if rotated {
		t.Fatal("expired refresh token rotated credentials")
	}
	rotated, err = nodes.RotateNodeCredentials(ctx, node.ID, node.RefreshTokenHash, now, next)
	mustPersist(t, err)
	if !rotated {
		t.Fatal("current refresh token did not rotate credentials")
	}
	rotated, err = nodes.RotateNodeCredentials(ctx, node.ID, node.RefreshTokenHash, now, model.NodeCredentials{})
	mustPersist(t, err)
	if rotated {
		t.Fatal("previous refresh token rotated credentials again")
	}
	if _, err := nodes.FindNodeForAccess(ctx, node.ID, node.AccessTokenHash, now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("previous access token is still valid: %v", err)
	}
	current, err = nodes.FindNodeForAccess(ctx, node.ID, next.AccessTokenHash, now)
	mustPersist(t, err)
	if current.SessionGeneration != node.SessionGeneration || current.RefreshTokenHash != next.RefreshTokenHash {
		t.Fatal("rotation did not preserve the session or install the next refresh token")
	}
	receipt, err := nodes.FindRefreshReceipt(ctx, node.ID, node.RefreshTokenHash, next.RefreshRequestHash, now)
	mustPersist(t, err)
	if string(receipt) != string(next.RefreshReceipt) {
		t.Fatal("rotation did not store the refresh receipt")
	}
	if _, err := nodes.FindRefreshReceipt(ctx, node.ID, node.RefreshTokenHash, "wrong-request", now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("different request retrieved the refresh receipt: %v", err)
	}
	if _, err := nodes.FindRefreshReceipt(ctx, node.ID, node.RefreshTokenHash, next.RefreshRequestHash, next.RefreshRetryUntil); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expired refresh receipt is still available: %v", err)
	}

	if err := nodes.RevokeNodeCredentials(ctx, node.ID+1); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("revoking credentials for an absent node: %v", err)
	}
	mustPersist(t, nodes.RevokeNodeCredentials(ctx, node.ID))
	current, err = nodes.FindByID(node.ID)
	mustPersist(t, err)
	if current.SessionGeneration != node.SessionGeneration+1 || current.Username != node.Username || current.Password != node.Password ||
		current.RegistrationTokenHash != "" || current.RegistrationExpiresAt != nil {
		t.Fatal("revocation did not advance the generation and clear registration while preserving local credentials")
	}
	if _, err := nodes.FindNodeForAccess(ctx, node.ID, next.AccessTokenHash, now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("revoked access token is still valid: %v", err)
	}
	rotated, err = nodes.RotateNodeCredentials(ctx, node.ID, next.RefreshTokenHash, now, next)
	mustPersist(t, err)
	if rotated {
		t.Fatal("revoked refresh token rotated credentials")
	}
	if _, err := nodes.FindRefreshReceipt(ctx, node.ID, node.RefreshTokenHash, next.RefreshRequestHash, now); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("revoked node still has a refresh receipt: %v", err)
	}
}
