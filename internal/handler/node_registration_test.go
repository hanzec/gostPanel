package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/service"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"
)

type nodeAuthFixture struct {
	db      *gorm.DB
	handler *ReportHandler
	server  *httptest.Server
	nodes   *service.NodeService
	key     []byte
}

func newNodeAuthFixture(t *testing.T) nodeAuthFixture {
	t.Helper()
	db := newHandlerHTTPFixture(t).db
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	handler := NewReportHandler(db, false)
	key := bytes.Repeat([]byte{0x6f}, 32)
	if err := handler.SetNodeTokenKey(key); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+nodegrpc.RegisterPath, handler.RegisterHTTP)
	mux.HandleFunc("POST "+nodegrpc.RefreshPath, handler.RefreshHTTP)
	mux.HandleFunc("GET "+nodegrpc.SessionPath, handler.SessionHTTP)
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	return nodeAuthFixture{db, handler, server, service.NewNodeService(db, ""), key}
}

func (f nodeAuthFixture) issue(t *testing.T, nodeID uint) string {
	t.Helper()
	issued, err := f.nodes.IssueRegistrationToken(t.Context(), nodeID, "admin", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(issued.ExpiresAt); remaining < 14*time.Minute || remaining > 15*time.Minute {
		t.Fatalf("token lifetime = %v", remaining)
	}
	return issued.Token
}

func (f nodeAuthFixture) register(ctx context.Context, request *nodegrpc.RegisterNodeRequest) (*nodegrpc.RegisterNodeReply, error) {
	reply := &nodegrpc.RegisterNodeReply{}
	err := nodegrpc.PostProtobuf(ctx, f.server.Client(), f.server.URL+nodegrpc.RegisterPath, request, reply)
	return reply, err
}

func (f nodeAuthFixture) refresh(ctx context.Context, request *nodegrpc.RefreshNodeRequest) (*nodegrpc.NodeCredentials, error) {
	reply := &nodegrpc.NodeCredentials{}
	err := nodegrpc.PostProtobuf(ctx, f.server.Client(), f.server.URL+nodegrpc.RefreshPath, request, reply)
	return reply, err
}

func (f nodeAuthFixture) dial(ctx context.Context, nodeID uint64, token string) (*nodegrpc.WebSocketStream, error) {
	client := *f.server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, f.server.Listener.Addr().String())
	}
	client.Transport = transport
	host, err := nodegrpc.ParsePanelHost("127.0.0.1")
	if err != nil {
		return nil, err
	}
	return host.DialSession(ctx, &client, nodeID, token)
}

func requireNodeHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	var response *nodegrpc.HTTPStatusError
	if !errors.As(err, &response) || response.StatusCode != want {
		t.Fatalf("node HTTP error = %v, want HTTP %d", err, want)
	}
}

func (f nodeAuthFixture) startSession(t *testing.T, ctx context.Context, id uint64, token string) nodegrpc.V1Client {
	t.Helper()
	stream, err := f.dial(ctx, id, token)
	if err != nil {
		t.Fatal(err)
	}
	session := nodegrpc.NewSession(stream, nil)
	t.Cleanup(func() {
		session.Close()
		_ = stream.Close()
	})
	go func() { _ = session.Run() }()
	return nodegrpc.NewV1Client(session)
}

func TestNodeRegistration(t *testing.T) {
	f := newNodeAuthFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	node := model.GostNode{Name: "registered node", Port: 39000}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	assertRejected := func(token string, want int) {
		t.Helper()
		_, err := f.register(ctx, &nodegrpc.RegisterNodeRequest{Token: token})
		requireNodeHTTPStatus(t, err, want)
	}
	assertRejected("invalid", http.StatusUnauthorized)
	expired := f.issue(t, node.ID)
	if err := f.db.Model(&node).Update("registration_expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	assertRejected(expired, http.StatusUnauthorized)
	replaced := f.issue(t, node.ID)
	token := f.issue(t, node.ID)
	assertRejected(replaced, http.StatusUnauthorized)
	if err := f.db.First(&node).Error; err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(token))
	if node.RegistrationTokenHash != hex.EncodeToString(digest[:]) || node.Password != "" {
		t.Fatal("pending registration should store the registration hash without issuing credentials")
	}

	reply, err := f.register(ctx, &nodegrpc.RegisterNodeRequest{Token: token})
	if err != nil || reply.GetNodeId() != uint64(node.ID) || reply.GetUsername() == "" ||
		reply.GetPassword() == "" || reply.GetCredentials().GetAccessToken() == "" || reply.GetCredentials().GetRefreshToken() == "" {
		t.Fatalf("registration failed to return node credentials: %v", err)
	}
	node.Remark = "updated metadata"
	if err := repository.NewNodeRepository(f.db).Update(&node, nil, nil); err != nil {
		t.Fatal(err)
	}
	assertRejected(token, http.StatusUnauthorized)
	var enrolled model.GostNode
	if err := f.db.First(&enrolled, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if enrolled.Remark != node.Remark || enrolled.RegistrationTokenHash != "" || enrolled.RegistrationExpiresAt != nil ||
		enrolled.Username != reply.GetUsername() || enrolled.Password != reply.GetPassword() || enrolled.SessionGeneration != 1 {
		t.Fatal("stored registration state does not match the successful reply")
	}
	if enrolled.Address != "127.0.0.1" || enrolled.IPv4 != "127.0.0.1" || enrolled.IPv6 != "" {
		t.Fatal("registration did not store the direct peer address")
	}
	data, err := json.Marshal(enrolled)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{enrolled.Password, enrolled.AccessTokenHash, enrolled.RefreshTokenHash} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("node JSON exposes credentials")
		}
	}

	access := reply.GetCredentials().GetAccessToken()
	_, err = f.dial(ctx, reply.GetNodeId(), "")
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	firstStream := f.startSession(t, ctx, reply.GetNodeId(), access)
	report := &nodegrpc.ReportRequest{NodeId: reply.GetNodeId(), Observation: &observer.ObserveRequest{}}
	if _, err := firstStream.Report(ctx, report); err != nil {
		t.Fatalf("report using issued credentials: %v", err)
	}

	request := &nodegrpc.RegisterNodeRequest{Token: f.issue(t, node.ID), Ipv4: "2001:db8::7"}
	_, err = f.register(ctx, request)
	requireNodeHTTPStatus(t, err, http.StatusBadRequest)
	request.Ipv4, request.Ipv6 = "", "2001:0db8:0:0::7"
	if err := f.db.Model(&node).Update("status", model.NodeStatusOnline).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.register(ctx, request)
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	if _, err := firstStream.Report(ctx, report); err != nil {
		t.Fatalf("rejected re-enrollment interrupted the current session: %v", err)
	}
	if err := f.db.Model(&node).Update("status", model.NodeStatusOffline).Error; err != nil {
		t.Fatal(err)
	}
	type result struct {
		reply *nodegrpc.RegisterNodeReply
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			reply, err := f.register(ctx, request)
			results <- result{reply, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil {
		t.Fatalf("neither concurrent registration succeeded: %v / %v", first.err, second.err)
	}
	requireNodeHTTPStatus(t, second.err, http.StatusUnauthorized)
	if err := f.db.First(&enrolled, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	if enrolled.IPv4 != "" || enrolled.IPv6 != "2001:db8::7" || enrolled.Address != enrolled.IPv6 ||
		enrolled.SessionGeneration != 2 || enrolled.Password != reply.GetPassword() || first.reply.GetPassword() != reply.GetPassword() {
		t.Fatal("re-enrollment must normalize the IP and rotate public credentials while preserving Gost credentials")
	}
	if _, err := firstStream.Report(ctx, report); err == nil {
		t.Fatal("previous session survived re-enrollment")
	}
	_, err = f.dial(ctx, reply.GetNodeId(), access)
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	currentStream := f.startSession(t, ctx, reply.GetNodeId(), first.reply.GetCredentials().GetAccessToken())
	if _, err := currentStream.Report(ctx, report); err != nil {
		t.Fatalf("report using rotated credentials: %v", err)
	}

	ipUpdate := &nodegrpc.NodeIPsRequest{NodeId: uint64(node.ID), Ipv6: "2001:db8::8", Ipv4: "2001:db8::9"}
	if _, err := currentStream.UpdateNodeIPs(ctx, ipUpdate); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("IP update with wrong address family = %v", err)
	}
	assertIPs := func(address, ipv4, ipv6 string) {
		t.Helper()
		if err := f.db.First(&enrolled, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		if enrolled.Address != address || enrolled.IPv4 != ipv4 || enrolled.IPv6 != ipv6 {
			t.Fatalf("stored address, IPv4, IPv6 = %q, %q, %q; want %q, %q, %q", enrolled.Address, enrolled.IPv4, enrolled.IPv6, address, ipv4, ipv6)
		}
	}
	assertIPs("2001:db8::7", "", "2001:db8::7")
	ipUpdate.Ipv4 = ""
	if _, err := currentStream.UpdateNodeIPs(ctx, ipUpdate); err != nil {
		t.Fatalf("IP update using current credentials: %v", err)
	}
	assertIPs("2001:db8::8", "", "2001:db8::8")
	ipUpdate.Ipv4, ipUpdate.Ipv6 = "198.51.100.9", ""
	if _, err := currentStream.UpdateNodeIPs(ctx, ipUpdate); err != nil {
		t.Fatalf("IP update with IPv4 only: %v", err)
	}
	assertIPs("198.51.100.9", "198.51.100.9", "2001:db8::8")
	ipUpdate.Ipv4, ipUpdate.Ipv6 = "", "2001:db8::a"
	if _, err := currentStream.UpdateNodeIPs(ctx, ipUpdate); err != nil {
		t.Fatalf("IP update with IPv6 only: %v", err)
	}
	assertIPs("198.51.100.9", "198.51.100.9", "2001:db8::a")
	ipUpdate.Ipv6 = "2001:db8::7"
	if _, err := currentStream.UpdateNodeIPs(ctx, ipUpdate); err != nil {
		t.Fatalf("returning to the IP at the start of the session: %v", err)
	}
	assertIPs("198.51.100.9", "198.51.100.9", "2001:db8::7")
	if enrolled.Password != first.reply.GetPassword() || enrolled.RegistrationTokenHash != "" {
		t.Fatal("updating IPs changed the enrollment state")
	}
	config, err := currentStream.GetNodeConfig(ctx, &emptypb.Empty{})
	if err != nil || !json.Valid([]byte(config.GetNftTemplate())) ||
		!strings.Contains(config.GetNftTemplate(), nodegrpc.NftTableName(reply.GetNodeId())) ||
		!strings.Contains(string(config.GetConfigYaml()), "node_id: "+strconv.FormatUint(reply.GetNodeId(), 10)) {
		t.Fatalf("node did not receive its configuration and nftables template: %v", err)
	}
}

func TestNodeSessionReportsMultipleAddresses(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{
		Name: "multi-IP", Username: "multi-IP", Password: "password",
		Address: "198.51.100.9", IPv4: "198.51.100.9", IPv6: "2001:db8::7",
	}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session := startTestReportStream(t, ctx, startTestReportServer(t, f.db), node, nil)
	report := &nodegrpc.NodeIPsRequest{
		NodeId: uint64(node.ID), Ipv4: node.IPv4,
		Addresses: []string{"198.51.100.10", "::ffff:198.51.100.9", "198.51.100.10"},
	}
	if _, err := session.UpdateNodeIPs(ctx, report); err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.10", "198.51.100.9"}
	if node.Address != "198.51.100.9" || node.IPv6 != "" || !slices.Equal(node.Addresses, want) {
		t.Fatalf("unexpected node address snapshot: %q, %q, %v", node.Address, node.IPv6, node.Addresses)
	}
	report.Ipv4, report.Ipv6 = "", "2001:db8::7"
	report.Addresses = []string{report.Ipv6}
	if _, err := session.UpdateNodeIPs(ctx, report); err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	want = []string{"198.51.100.9", "2001:db8::7"}
	if node.Address != want[0] || node.IPv4 != want[0] || node.IPv6 != want[1] || !slices.Equal(node.Addresses, want) {
		t.Fatalf("IPv6-only report displaced known IPv4: %q, %q, %q, %v", node.Address, node.IPv4, node.IPv6, node.Addresses)
	}
	report.Addresses = []string{"fe80::1%eth0"}
	if _, err := session.UpdateNodeIPs(ctx, report); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid additional address was accepted: %v", err)
	}
}

func TestNodeRefreshRotationAndReplay(t *testing.T) {
	f := newNodeAuthFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	node := model.GostNode{Name: "refresh node"}
	other := model.GostNode{Name: "another node"}
	for _, fixture := range []*model.GostNode{&node, &other} {
		if err := f.db.Create(fixture).Error; err != nil {
			t.Fatal(err)
		}
	}
	reply, err := f.register(ctx, &nodegrpc.RegisterNodeRequest{Token: f.issue(t, node.ID)})
	if err != nil {
		t.Fatal(err)
	}
	issued := reply.GetCredentials()
	if until := time.Until(time.Unix(issued.GetAccessExpiresAt(), 0)); until < 14*time.Minute || until > 15*time.Minute {
		t.Fatalf("access credential lifetime = %v", until)
	}
	if until := time.Until(time.Unix(issued.GetRefreshExpiresAt(), 0)); until < 29*24*time.Hour || until > 30*24*time.Hour {
		t.Fatalf("refresh credential lifetime = %v", until)
	}
	session := f.startSession(t, ctx, reply.GetNodeId(), issued.GetAccessToken())
	report := &nodegrpc.ReportRequest{NodeId: reply.GetNodeId(), Observation: &observer.ObserveRequest{}}
	if _, err := session.Report(ctx, report); err != nil {
		t.Fatal(err)
	}

	request := &nodegrpc.RefreshNodeRequest{
		NodeId: reply.GetNodeId(), RefreshToken: issued.GetRefreshToken(), RequestId: "first-request",
	}
	_, err = f.refresh(ctx, &nodegrpc.RefreshNodeRequest{NodeId: uint64(other.ID), RefreshToken: request.RefreshToken, RequestId: request.RequestId})
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	type refreshResult struct {
		reply *nodegrpc.NodeCredentials
		err   error
	}
	results := make(chan refreshResult, 2)
	for range 2 {
		go func() {
			reply, err := f.refresh(ctx, request)
			results <- refreshResult{reply, err}
		}()
	}
	first, second := <-results, <-results
	rotated := first.reply
	if first.err != nil || second.err != nil || !proto.Equal(rotated, second.reply) ||
		rotated.GetAccessToken() == issued.GetAccessToken() || rotated.GetRefreshToken() == issued.GetRefreshToken() {
		t.Fatalf("concurrent refresh did not issue one rotated pair: %v, %v", first.err, second.err)
	}
	if _, err := session.Report(ctx, report); err != nil {
		t.Fatalf("refresh interrupted an authenticated session: %v", err)
	}
	_, err = f.dial(ctx, reply.GetNodeId(), issued.GetAccessToken())
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)

	// A fresh service instance must decrypt a committed response with the same key.
	restarted, err := service.NewNodeCredentialService(f.db, f.key)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Refresh(ctx, node.ID, request.RefreshToken, request.RequestId)
	if err != nil || !proto.Equal(replayed, rotated) {
		t.Fatalf("retry did not return the previously issued pair: %v", err)
	}
	_, err = f.refresh(ctx, &nodegrpc.RefreshNodeRequest{NodeId: reply.GetNodeId(), RefreshToken: request.RefreshToken, RequestId: "different"})
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)

	var stored model.GostNode
	if err := f.db.First(&stored, node.ID).Error; err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(rotated.GetAccessToken()))
	if stored.AccessTokenHash != hex.EncodeToString(digest[:]) ||
		stored.RefreshTokenHash == rotated.GetRefreshToken() || len(stored.RefreshReceipt) == 0 ||
		bytes.Contains(stored.RefreshReceipt, []byte(rotated.GetRefreshToken())) || stored.SessionGeneration != 1 {
		t.Fatal("credential storage must hash tokens, encrypt retries and preserve the session generation")
	}
	if err := f.db.Model(&node).Updates(map[string]any{
		"refresh_retry_until": time.Now().Add(-time.Second).Unix(),
		"access_expires_at":   time.Now().Add(-time.Second).Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	_, err = f.refresh(ctx, request)
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	_, err = f.dial(ctx, reply.GetNodeId(), rotated.GetAccessToken())
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	if _, err := session.Report(ctx, report); err != nil {
		t.Fatalf("access expiry interrupted an authenticated session: %v", err)
	}

	request.RefreshToken, request.RequestId = rotated.GetRefreshToken(), "second-request"
	rotated, err = f.refresh(ctx, request)
	if err != nil {
		t.Fatalf("refresh with an expired access token: %v", err)
	}
	pendingRegistration := f.issue(t, node.ID)
	if err := f.nodes.RevokeCredentials(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Report(ctx, report); err == nil {
		t.Fatal("revocation did not close the active session")
	}
	_, err = f.dial(ctx, reply.GetNodeId(), rotated.GetAccessToken())
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	_, err = f.refresh(ctx, request)
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	_, err = f.refresh(ctx, &nodegrpc.RefreshNodeRequest{
		NodeId: reply.GetNodeId(), RefreshToken: rotated.GetRefreshToken(), RequestId: "new-request",
	})
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
	_, err = f.register(ctx, &nodegrpc.RegisterNodeRequest{Token: pendingRegistration})
	requireNodeHTTPStatus(t, err, http.StatusUnauthorized)
}
