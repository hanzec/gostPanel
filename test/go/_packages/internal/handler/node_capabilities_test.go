package handler

import (
	"fmt"
	"testing"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNodeMPTCPReportsPersistAndReachAPI(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{"name":"MPTCP node","mptcp_supported":true}`, 200))
	path := fmt.Sprintf("/nodes/%d", node.ID)
	assertSupported := func(want bool) {
		t.Helper()
		var stored model.GostNode
		if err := f.db.First(&stored, node.ID).Error; err != nil {
			t.Fatal(err)
		}
		detail := handlerData[struct {
			Supported *bool `json:"mptcp_supported"`
		}](t, f.request(t, "GET", path, "", 200))
		list := handlerData[handlerPage[struct {
			Supported *bool `json:"mptcp_supported"`
		}]](t, f.request(t, "GET", "/nodes", "", 200))
		if stored.MPTCPSupported != want || detail.Supported == nil || *detail.Supported != want || len(list.List) != 1 || list.List[0].Supported == nil || *list.List[0].Supported != want {
			t.Fatalf("MPTCP support was not persisted and exposed as %t: database=%t detail=%+v list=%+v", want, stored.MPTCPSupported, detail, list)
		}
	}
	assertSupported(false)
	session := startTestReportStream(t, t.Context(), startTestReportServer(t, f.db), node, nil)
	supported, unsupported := true, false
	for _, step := range []struct {
		reported *bool
		ipv4     string
		want     bool
	}{
		{reported: &supported, want: true},
		{ipv4: "198.51.100.9", want: false},
		{reported: &supported, want: true},
		{reported: &unsupported, want: false},
	} {
		_, err := session.UpdateNodeIPs(t.Context(), &nodegrpc.NodeIPsRequest{NodeId: uint64(node.ID), Ipv4: step.ipv4, MptcpSupported: step.reported})
		if err != nil {
			t.Fatal(err)
		}
		assertSupported(step.want)
	}
	_, err := session.UpdateNodeIPs(t.Context(), &nodegrpc.NodeIPsRequest{NodeId: uint64(node.ID) + 1, MptcpSupported: &supported})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("capability report for another node = %v", err)
	}
	assertSupported(false)
}
