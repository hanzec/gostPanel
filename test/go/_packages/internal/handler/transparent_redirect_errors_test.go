package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
)

func TestCreateTransparentTunnelExplainsMissingDaemonRPC(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	// This fixture uses the real stream dispatcher without the newer redirect RPC.
	entry := f.enrollFixtureNode(t, "old entry daemon", &handlerNodeControl{})
	exit := f.enrollFixtureNode(t, "exit", &handlerNodeControl{})
	for _, node := range []*model.GostNode{&entry, &exit} {
		if err := f.db.Model(node).Update("ipv4", fmt.Sprintf("192.0.2.%d", node.ID)).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := handlerData[model.GostRule](t, f.request(t, "POST", "/rules",
		fmt.Sprintf(`{"name":"transparent entry","node_id":%d,"role":"entry","protocol":"red","listen_port":27000}`, entry.ID), http.StatusOK))
	destination := handlerData[model.GostRule](t, f.request(t, "POST", "/rules",
		fmt.Sprintf(`{"name":"original target exit","node_id":%d,"role":"exit","protocol":"tcp","listen_port":27001,"use_original_target":true}`, exit.ID), http.StatusOK))
	reply := f.request(t, "POST", "/tunnels", fmt.Sprintf(`{"name":"transparent","entry_rule_id":%d,"exit_rule_id":%d}`, source.ID, destination.ID), http.StatusNotImplemented)
	if reply.Code != bizerrors.ErrTunnelTransparentDaemon.Code || !strings.Contains(reply.Message, "升级入口节点的 yagp-daemon") || !strings.Contains(reply.Message, "升级后的进程已重新连接面板") {
		t.Fatalf("missing daemon RPC did not explain the required node action: %+v", reply)
	}
	var tunnels int64
	if err := f.db.Model(&model.GostTunnel{}).Count(&tunnels).Error; err != nil || tunnels != 0 {
		t.Fatalf("failed creation kept a tunnel: count=%d, error=%v", tunnels, err)
	}
	for _, endpoint := range []model.GostRule{source, destination} {
		stored := handlerData[model.GostRule](t, f.request(t, "GET", fmt.Sprintf("/rules/%d", endpoint.ID), "", http.StatusOK))
		if stored.Status != model.RuleStatusStopped || stored.ServiceID != "" {
			t.Fatalf("unsupported daemon left an endpoint running: %+v", stored)
		}
	}
}
