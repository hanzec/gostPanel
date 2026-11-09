package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/pkg/nodegrpc"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const (
	nodeAccessLifetime     = 15 * time.Minute
	nodeRefreshLifetime    = 30 * 24 * time.Hour
	nodeRefreshRetryWindow = 5 * time.Minute
)

type NodeCredentialService struct {
	nodes    *repository.NodeRepository
	receipts cipher.AEAD
}

func NewNodeCredentialService(db *gorm.DB, key []byte) (*NodeCredentialService, error) {
	if len(key) != 32 {
		return nil, errors.New("node token key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	receipts, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &NodeCredentialService{nodes: repository.NewNodeRepository(db), receipts: receipts}, nil
}

func (s *NodeCredentialService) Enroll(ctx context.Context, id uint, token, ipv4, ipv6 string) (*nodegrpc.RegisterNodeReply, error) {
	address := ipv4
	if address == "" {
		address = ipv6
	}
	node := &model.GostNode{
		ID: id, Address: address, IPv4: ipv4, IPv6: ipv6,
		Username: fmt.Sprintf("node-%d", id), Password: newNodeSecret(),
	}
	issued, stored := issueNodeCredentials(time.Now())
	if err := s.nodes.EnrollNode(ctx, node, registrationTokenHash(token), stored); err != nil {
		return nil, err
	}
	return &nodegrpc.RegisterNodeReply{
		NodeId: uint64(node.ID), Username: node.Username, Password: node.Password, Credentials: issued,
	}, nil
}

func (s *NodeCredentialService) AuthenticateAccess(ctx context.Context, id uint, token string) (*model.GostNode, error) {
	return s.nodes.FindNodeForAccess(ctx, id, registrationTokenHash(token), time.Now().Unix())
}

func (s *NodeCredentialService) Refresh(ctx context.Context, id uint, token, requestID string) (*nodegrpc.NodeCredentials, error) {
	now := time.Now()
	tokenHash, requestHash := registrationTokenHash(token), registrationTokenHash(requestID)
	binding := fmt.Appendf(nil, "%d:%s:%s", id, tokenHash, requestHash)
	issued, next := issueNodeCredentials(now)
	payload, err := proto.Marshal(issued)
	if err != nil {
		return nil, err
	}
	next.PreviousRefreshTokenHash = tokenHash
	next.RefreshRequestHash = requestHash
	next.RefreshReceipt = s.receipts.Seal(nil, nil, payload, binding)
	next.RefreshRetryUntil = now.Add(nodeRefreshRetryWindow).Unix()
	rotated, err := s.nodes.RotateNodeCredentials(ctx, id, tokenHash, now.Unix(), next)
	if err != nil {
		return nil, err
	}
	if rotated {
		return issued, nil
	}
	// The old token is accepted only to retrieve its already committed response.
	receipt, err := s.nodes.FindRefreshReceipt(ctx, id, tokenHash, requestHash, now.Unix())
	if err != nil {
		return nil, err
	}
	payload, err = s.receipts.Open(nil, nil, receipt, binding)
	if err != nil {
		return nil, err
	}
	issued = &nodegrpc.NodeCredentials{}
	if err := proto.Unmarshal(payload, issued); err != nil {
		return nil, err
	}
	return issued, nil
}

func issueNodeCredentials(now time.Time) (*nodegrpc.NodeCredentials, model.NodeCredentials) {
	access, refresh := newNodeSecret(), newNodeSecret()
	issued := &nodegrpc.NodeCredentials{
		AccessToken: access, RefreshToken: refresh,
		AccessExpiresAt: now.Add(nodeAccessLifetime).Unix(), RefreshExpiresAt: now.Add(nodeRefreshLifetime).Unix(),
	}
	return issued, model.NodeCredentials{
		AccessTokenHash: registrationTokenHash(access), RefreshTokenHash: registrationTokenHash(refresh),
		AccessExpiresAt: issued.AccessExpiresAt, RefreshExpiresAt: issued.RefreshExpiresAt,
	}
}
