package handler

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
)

func TestNodeAuthBodiesTimeOutWithoutExpiringSession(t *testing.T) {
	f := newNodeAuthFixture(t)
	node := model.GostNode{Name: "connected"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), nodeAuthBodyTimeout+5*time.Second)
	defer cancel()
	reply, err := f.register(ctx, &nodegrpc.RegisterNodeRequest{Token: f.issue(t, node.ID)})
	if err != nil {
		t.Fatal(err)
	}
	session := f.startSession(t, ctx, reply.NodeId, reply.Credentials.AccessToken)
	request := &nodegrpc.ReportRequest{NodeId: reply.NodeId, Observation: &observer.ObserveRequest{}}
	if _, err := session.Report(ctx, request); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+nodegrpc.RegisterPath, f.handler.RegisterHTTP)
	mux.HandleFunc("POST "+nodegrpc.RefreshPath, f.handler.RefreshHTTP)
	server := httptest.NewServer(mux)
	defer server.Close()
	var connections []net.Conn
	cases := []struct {
		path, contentType string
		want              int
	}{
		{nodegrpc.RegisterPath, nodegrpc.ProtobufContentType, http.StatusRequestTimeout},
		{nodegrpc.RefreshPath, nodegrpc.ProtobufContentType, http.StatusRequestTimeout},
		{nodegrpc.RegisterPath, "application/json", http.StatusUnsupportedMediaType},
		{nodegrpc.RefreshPath, "application/json", http.StatusUnsupportedMediaType},
	}
	for _, test := range cases {
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(nodeAuthBodyTimeout + 3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nContent-Length: 1\r\n\r\n", test.path, test.contentType); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	for i, conn := range connections {
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("body deadline did not produce a response: %v", err)
		}
		response.Body.Close()
		if response.StatusCode != cases[i].want {
			t.Fatalf("%s %s slow body returned HTTP %d", cases[i].path, cases[i].contentType, response.StatusCode)
		}
	}
	if _, err := session.Report(ctx, request); err != nil {
		t.Fatalf("authentication body deadline expired an active WebSocket: %v", err)
	}
}
