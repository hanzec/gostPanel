package repository

import (
	"context"

	"yagp/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func (r *NodeRepository) FindNodeForAccess(ctx context.Context, id uint, hash string, now int64) (*model.GostNode, error) {
	var node model.GostNode
	err := r.DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).
		Where("id = ? AND access_token_hash = ? AND access_expires_at > ?", id, hash, now).
		First(&node).Error
	return &node, err
}

func (r *NodeRepository) RotateNodeCredentials(ctx context.Context, id uint, currentHash string, now int64, next model.NodeCredentials) (bool, error) {
	result := r.DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).Model(&model.GostNode{}).
		Where("id = ? AND refresh_token_hash = ? AND refresh_expires_at > ?", id, currentHash, now).
		UpdateColumns(nodeCredentialValues(next))
	return result.RowsAffected == 1, result.Error
}

func (r *NodeRepository) FindRefreshReceipt(ctx context.Context, id uint, previousHash, requestHash string, now int64) ([]byte, error) {
	var node model.GostNode
	err := r.DB.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}).
		Select("refresh_receipt").
		Where("id = ? AND previous_refresh_token_hash = ? AND refresh_request_hash = ? AND refresh_retry_until > ?",
			id, previousHash, requestHash, now).
		Take(&node).Error
	return node.RefreshReceipt, err
}

func (r *NodeRepository) RevokeNodeCredentials(ctx context.Context, id uint) error {
	values := nodeCredentialValues(model.NodeCredentials{})
	values["session_generation"] = gorm.Expr("session_generation + 1")
	values["registration_token_hash"] = ""
	values["registration_expires_at"] = nil
	result := r.DB.WithContext(ctx).Model(&model.GostNode{}).Where("id = ?", id).UpdateColumns(values)
	if result.Error == nil && result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return result.Error
}

func nodeCredentialValues(credentials model.NodeCredentials) map[string]any {
	return map[string]any{
		"access_token_hash":           credentials.AccessTokenHash,
		"access_expires_at":           credentials.AccessExpiresAt,
		"refresh_token_hash":          credentials.RefreshTokenHash,
		"refresh_expires_at":          credentials.RefreshExpiresAt,
		"previous_refresh_token_hash": credentials.PreviousRefreshTokenHash,
		"refresh_request_hash":        credentials.RefreshRequestHash,
		"refresh_receipt":             credentials.RefreshReceipt,
		"refresh_retry_until":         credentials.RefreshRetryUntil,
	}
}
