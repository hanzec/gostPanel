package nodebridge

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDetectMultipathTCPMatchesLoopbackConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	listenConfig := net.ListenConfig{}
	listenConfig.SetMultipathTCP(true)
	listener, err := listenConfig.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dialer := net.Dialer{}
	dialer.SetMultipathTCP(true)
	conn, err := dialer.DialContext(ctx, "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	active, err := conn.(*net.TCPConn).MultipathTCP()
	if err != nil {
		t.Fatal(err)
	}
	if got := detectMultipathTCP(); got != active {
		t.Fatalf("MPTCP detection = %t, real loopback connection = %t", got, active)
	}
}
