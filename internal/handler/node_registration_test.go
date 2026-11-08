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
	"slices"
	"strings"
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
	if err := db.AutoMigrate(&model.GostNode{}, &model.OperationLog{}, &model.GostAllowlist{}); err != nil {
		t.Fatal(err)
	}
	node := model.GostNode{Name: "test", Port: 39000}
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
	if err := repository.NewNodeRepository(db).Update(&node, nil, nil); err != nil {
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
	if enrolled.Address != "127.0.0.1" || enrolled.IPv4 != "127.0.0.1" || enrolled.IPv6 != "" {
		t.Fatal("registration did not store the direct peer address for automatic control")
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
	if publicNode["ipv4"] != enrolled.IPv4 || publicNode["ipv6"] != enrolled.IPv6 {
		t.Fatal("node JSON does not expose the detected addresses")
	}
	initialAuth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(reply.GetUsername(), reply.GetPassword()))
	report := &nodegrpc.ReportRequest{NodeId: reply.GetNodeId(), Observation: &observer.ObserveRequest{}}
	if _, err := client.Report(ctx, report, initialAuth); err != nil {
		t.Fatalf("report using issued credentials: %v", err)
	}

	request.Token = issue()
	request.Ipv4 = "2001:db8::7"
	if _, err := client.RegisterNode(ctx, request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("register with wrong address family = %v", err)
	}
	request.Ipv4 = ""
	request.Ipv6 = "2001:0db8:0:0::7"
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
	if err := db.First(&enrolled, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if enrolled.IPv4 != "" || enrolled.IPv6 != "2001:db8::7" || enrolled.Address != enrolled.IPv6 {
		t.Fatal("re-registration did not replace the address with the normalized IPv6")
	}
	assertRejected(request.Token, certPEM, codes.Unauthenticated)
	if _, err := client.Report(ctx, report, initialAuth); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("report using rotated credentials = %v", err)
	}
	newAuth := grpc.PerRPCCredentials(nodegrpc.PasswordCredentials(first.reply.GetUsername(), first.reply.GetPassword()))
	if _, err := client.Report(ctx, report, newAuth); err != nil {
		t.Fatalf("report using refreshed credentials: %v", err)
	}
	allowlist := service.NewGostAllowlistService(db)
	if _, err := allowlist.Update(ctx, []uint{node.ID}); err != nil {
		t.Fatal(err)
	}
	ipUpdate := &nodegrpc.NodeIPsRequest{NodeId: uint64(node.ID), Ipv6: "2001:db8::8"}
	if _, err := client.UpdateNodeIPs(ctx, ipUpdate); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("IP update without credentials = %v", err)
	}
	if _, err := client.UpdateNodeIPs(ctx, ipUpdate, initialAuth); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("IP update using rotated credentials = %v", err)
	}
	ipUpdate.Ipv4 = "2001:db8::9"
	if _, err := client.UpdateNodeIPs(ctx, ipUpdate, newAuth); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("IP update with wrong address family = %v", err)
	}
	assertIPs := func(address, ipv4, ipv6 string, allowed []string) {
		t.Helper()
		if err := db.First(&enrolled, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if enrolled.Address != address || enrolled.IPv4 != ipv4 || enrolled.IPv6 != ipv6 {
			t.Fatalf("stored address, IPv4, IPv6 = %q, %q, %q; want %q, %q, %q", enrolled.Address, enrolled.IPv4, enrolled.IPv6, address, ipv4, ipv6)
		}
		config, err := allowlist.Get(ctx)
		if err != nil || !slices.Equal(config.NodeIDs, []uint{node.ID}) || !slices.Equal(config.Addresses, allowed) {
			t.Fatalf("resolved allowlist = %+v, err = %v; want %v for the same node", config, err, allowed)
		}
	}
	assertIPs("2001:db8::7", "", "2001:db8::7", []string{"2001:db8::7"})
	ipUpdate.Ipv4 = ""
	if _, err := client.UpdateNodeIPs(ctx, ipUpdate, newAuth); err != nil {
		t.Fatalf("IP update using current credentials: %v", err)
	}
	assertIPs("2001:db8::8", "", "2001:db8::8", []string{"2001:db8::8"})
	ipUpdate.Ipv4, ipUpdate.Ipv6 = "198.51.100.9", ""
	if _, err := client.UpdateNodeIPs(ctx, ipUpdate, newAuth); err != nil {
		t.Fatalf("IP update with IPv4 only: %v", err)
	}
	assertIPs("198.51.100.9", "198.51.100.9", "2001:db8::8", []string{"198.51.100.9", "2001:db8::8"})
	ipUpdate.Ipv4, ipUpdate.Ipv6 = "", "2001:db8::a"
	if _, err := client.UpdateNodeIPs(ctx, ipUpdate, newAuth); err != nil {
		t.Fatalf("IP update with IPv6 only: %v", err)
	}
	assertIPs("198.51.100.9", "198.51.100.9", "2001:db8::a", []string{"198.51.100.9", "2001:db8::a"})
	if enrolled.Password != first.reply.GetPassword() || enrolled.TLSCertificatePEM != certPEM || enrolled.RegistrationTokenHash != "" {
		t.Fatal("updating IPs changed the enrollment state")
	}
	requestTemplate := &nodegrpc.NftTemplateRequest{NodeId: reply.GetNodeId()}
	if _, err := client.GetNftTemplate(ctx, requestTemplate); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated nft template request: %v", err)
	}
	template, err := client.GetNftTemplate(ctx, requestTemplate, newAuth)
	if err != nil || !json.Valid([]byte(template.GetConfig())) ||
		!strings.Contains(template.GetConfig(), nodegrpc.NftTableName(reply.GetNodeId())) {
		t.Fatalf("node did not receive its nftables JSON template: %v", err)
	}
	if err := db.Model(&enrolled).Update("tls_certificate_pem", "").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Report(ctx, report, newAuth); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("report with credentials but no registration certificate = %v", err)
	}
	if _, err := client.GetNftTemplate(ctx, requestTemplate, newAuth); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("nft template request with credentials but no registration certificate = %v", err)
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
