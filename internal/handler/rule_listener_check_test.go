package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"yagp/internal/model"
	"yagp/internal/service"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type listenerCheckNode struct {
	nodegrpc.UnimplementedV1Server
	calls chan *nodegrpc.ListenerCheckRequest
	reply chan error
}

func (n listenerCheckNode) CheckListener(_ context.Context, req *nodegrpc.ListenerCheckRequest) (*nodegrpc.ListenerCheckReply, error) {
	n.calls <- req
	if err := <-n.reply; err != nil {
		return nil, err
	}
	return &nodegrpc.ListenerCheckReply{Reason: "occupied"}, nil
}

func TestEntranceHTTPChecksSavedListenerOnItsNode(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	h := NewRuleHandler(service.NewRuleService(f.db))
	f.engine.POST("/rules", h.Create)
	f.engine.POST("/rules/:id/listener-check", h.CheckListener)
	node := model.GostNode{
		Name: "listener node", Status: model.NodeStatusOnline, Username: "listener-node", Password: "test-password",
	}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	remote := listenerCheckNode{calls: make(chan *nodegrpc.ListenerCheckRequest, 4), reply: make(chan error, 1)}
	dispatch := nodegrpc.NewDispatcher(remote, nodegrpc.V1_CheckListener_FullMethodName)
	startTestReportStream(t, t.Context(), startTestReportServer(t, f.db), node, dispatch)

	create := fmt.Sprintf(`{"name":"entry","node_id":%d,"protocol":"udp","listen_ip":"127.0.0.1","listen_port":16047}`, node.ID)
	rule := handlerData[model.GostRule](t, f.request(t, http.MethodPost, "/rules", create, http.StatusOK))
	if len(remote.calls) != 0 || rule.Status != model.RuleStatusStopped {
		t.Fatalf("saving unexpectedly used the node: %+v", rule)
	}

	path := fmt.Sprintf("/rules/%d/listener-check", rule.ID)
	remote.reply <- nil
	result := handlerData[listenerCheckResult](t, f.request(t, http.MethodPost, path, "", http.StatusOK))
	if result.Available || result.Reason != "occupied" {
		t.Fatalf("check = %+v", result)
	}
	select {
	case req := <-remote.calls:
		if req.GetProtocol() != "udp" || req.GetIp() != "127.0.0.1" || req.GetPort() != 16047 {
			t.Fatalf("node checked the wrong binding: %v", req)
		}
	default:
		t.Fatal("the node received no listener check")
	}
	if err := f.db.First(&rule, rule.ID).Error; err != nil || rule.Status != model.RuleStatusStopped {
		t.Fatalf("occupied listener changed the saved entrance: %+v (%v)", rule, err)
	}
	for _, transport := range []struct{ protocol, network string }{
		{"tls", "tcp"}, {"mtls", "tcp"}, {"ws", "tcp"}, {"mws", "tcp"}, {"quic", "udp"},
	} {
		if err := f.db.Model(&rule).Update("protocol", transport.protocol).Error; err != nil {
			t.Fatal(err)
		}
		remote.reply <- nil
		f.request(t, http.MethodPost, path, "", http.StatusOK)
		if req := <-remote.calls; req.Protocol != transport.network {
			t.Fatalf("%s listener check used %s instead of %s", transport.protocol, req.Protocol, transport.network)
		}
	}

	remote.reply <- status.Error(codes.Unimplemented, "old daemon")
	f.request(t, http.MethodPost, path, "", http.StatusNotImplemented)
	<-remote.calls
	remote.reply <- status.Error(codes.Unavailable, "disconnected")
	f.request(t, http.MethodPost, path, "", http.StatusBadGateway)
	<-remote.calls

	if err := f.db.Model(&rule).Update("status", model.RuleStatusRunning).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, path, "", http.StatusConflict)
	f.request(t, http.MethodPost, "/rules/0/listener-check", "", http.StatusBadRequest)
	f.request(t, http.MethodPost, "/rules/999/listener-check", "", http.StatusNotFound)
	legacy := model.GostRule{Name: "old tunnel entrance", Type: model.RuleTypeTunnel, Protocol: model.RuleProtocolTCP, ListenPort: 16048}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, fmt.Sprintf("/rules/%d/listener-check", legacy.ID), "", http.StatusBadRequest)

	if err := f.db.Model(&rule).Update("status", model.RuleStatusStopped).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&node).Update("status", model.NodeStatusOffline).Error; err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodPost, path, "", http.StatusBadRequest)
	if len(remote.calls) != 0 {
		t.Fatal("an invalid check was sent to the node")
	}
}
