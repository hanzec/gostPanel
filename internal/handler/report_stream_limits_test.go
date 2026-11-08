package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"yagp/internal/model"
	"yagp/pkg/gost"
	"yagp/pkg/nodegrpc"

	observer "github.com/go-gost/plugin/observer/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReportStreamAllowsLargeCommandReplyWhileTrafficReportsAreThrottled(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	node := model.GostNode{Name: "registered", Username: "node", Password: "secret"}
	if err := f.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	const config = `{"services":[{"name":"edge-service"}]}`
	// Keep the config at the daemon's 4 MiB HTTP response limit, before protobuf framing.
	body := strings.Repeat(" ", 4<<20-len(config)) + config
	commandStarted := make(chan struct{})
	allowReply := make(chan struct{})
	control := &handlerNodeControl{admin: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/config" {
			t.Errorf("unexpected Gost admin request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		close(commandStarted)
		select {
		case <-allowReply:
			_, _ = io.WriteString(w, body)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusGatewayTimeout)
		}
	})}
	client := startTestReportServer(t, f.db, grpc.MaxRecvMsgSize(nodegrpc.MaxStreamFrameBytes))
	reports := startTestReportStream(t, ctx, client, node, nodegrpc.NewDispatcher(control, nodegrpc.V1_Execute_FullMethodName))
	commandResult := make(chan error, 1)
	go func() {
		panel := gost.NewClient(&gost.Config{NodeID: node.ID, Username: node.Username, Password: node.Password})
		cfg, err := panel.GetConfig()
		if err == nil && (len(cfg.Services) != 1 || cfg.Services[0].Name != "edge-service") {
			err = fmt.Errorf("unexpected Gost config: %+v", cfg)
		}
		commandResult <- err
	}()
	select {
	case <-commandStarted:
	case err := <-commandResult:
		t.Fatalf("reverse command did not reach the daemon: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	request := &nodegrpc.ReportRequest{NodeId: uint64(node.ID), Observation: &observer.ObserveRequest{}}
	throttled := false
	for i := range 2 * trafficReportBurst {
		reply, err := reports.Report(ctx, request)
		if i > 0 && status.Code(err) == codes.ResourceExhausted {
			throttled = true
			break
		}
		if err != nil || !reply.GetOk() {
			t.Fatalf("traffic report %d = %v, %v", i+1, reply, err)
		}
	}
	if !throttled {
		t.Fatal("traffic reports were not throttled")
	}
	close(allowReply)
	select {
	case err := <-commandResult:
		if err != nil {
			t.Fatalf("reverse command reply after traffic throttling: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
