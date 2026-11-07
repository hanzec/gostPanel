package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/nodegrpc"

	"github.com/glebarez/sqlite"
	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestNodeRegistration(t *testing.T) {
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "nodes.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.GostNode{}, &model.OperationLog{}); err != nil {
		t.Fatal(err)
	}
	node := model.GostNode{Name: "test", Address: "localhost", Port: 39000}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	client := startTestReportServer(t, db)
	nodes := service.NewNodeService(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	certPEM := testNodeCertificate(t)
	issue := func() string {
		t.Helper()
		issued, err := nodes.IssueRegistrationToken(ctx, node.ID, "admin", "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		if remaining := time.Until(issued.ExpiresAt); remaining < 14*time.Minute || remaining > 15*time.Minute {
			t.Fatalf("token lifetime = %v", remaining)
		}
		return issued.Token
	}
	assertRejected := func(token, certificate string, code codes.Code) {
		t.Helper()
		_, err := client.RegisterNode(ctx, &nodegrpc.RegisterNodeRequest{Token: token, NodeCertificatePem: certificate})
		if status.Code(err) != code {
			t.Fatalf("register = %v, want %v", err, code)
		}
	}

	expired := issue()
	if err := db.Model(&node).Update("registration_expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	assertRejected(expired, certPEM, codes.Unauthenticated)
	replaced := issue()
	token := issue()
	assertRejected(replaced, certPEM, codes.Unauthenticated)
	assertRejected(token, "invalid certificate", codes.InvalidArgument)
	if err := db.First(&node).Error; err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	if node.RegistrationTokenHash != hex.EncodeToString(digest[:]) || node.Password != "" {
		t.Fatal("pending registration stores the token hash and no password")
	}

	request := &nodegrpc.RegisterNodeRequest{Token: token, NodeCertificatePem: certPEM}
	reply, err := client.RegisterNode(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if reply.GetNodeId() != uint64(node.ID) || reply.GetControlPort() != uint32(node.Port) || reply.GetUsername() == "" || reply.GetPassword() == "" {
		t.Fatal("registration did not return the node identity, port and credentials")
	}
	// Saving metadata read before registration must not restore the consumed token.
	node.Remark = "updated metadata"
	if err := repository.NewNodeRepository(db).Update(&node); err != nil {
		t.Fatal(err)
	}
	assertRejected(token, certPEM, codes.Unauthenticated)
	var enrolled model.GostNode
	if err := db.First(&enrolled, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if enrolled.Remark != node.Remark || enrolled.RegistrationTokenHash != "" || enrolled.RegistrationExpiresAt != nil || enrolled.TLSCertificatePEM != certPEM ||
		enrolled.Username != reply.GetUsername() || enrolled.Password != reply.GetPassword() {
		t.Fatal("stored registration state does not match the successful reply")
	}
	data, err := json.Marshal(enrolled)
	if err != nil {
		t.Fatal(err)
	}
	var publicNode map[string]any
	if err := json.Unmarshal(data, &publicNode); err != nil {
		t.Fatal(err)
	}
	if _, ok := publicNode["password"]; ok {
		t.Fatal("node JSON exposes the password")
	}
	initialAuth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(reply.GetUsername(), reply.GetPassword()))
	report := &nodegrpc.ReportRequest{NodeId: reply.GetNodeId(), Observation: &observer.ObserveRequest{}}
	if _, err := client.Report(ctx, report, initialAuth); err != nil {
		t.Fatalf("report using issued credentials: %v", err)
	}

	request.Token = issue()
	type result struct {
		reply *nodegrpc.RegisterNodeReply
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			reply, err := client.RegisterNode(ctx, request)
			results <- result{reply, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil || status.Code(second.err) != codes.Unauthenticated {
		t.Fatalf("concurrent registrations = %v and %v, want exactly one success", first.err, second.err)
	}
	assertRejected(request.Token, certPEM, codes.Unauthenticated)
	if _, err := client.Report(ctx, report, initialAuth); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("report using rotated credentials = %v", err)
	}
	newAuth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(first.reply.GetUsername(), first.reply.GetPassword()))
	if _, err := client.Report(ctx, report, newAuth); err != nil {
		t.Fatalf("report using refreshed credentials: %v", err)
	}
}

func testNodeCertificate(t *testing.T) string {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{nodegrpc.NodeCertificateName},
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
