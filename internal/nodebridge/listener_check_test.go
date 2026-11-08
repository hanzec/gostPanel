package nodebridge

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestCheckListenerUsesRealSockets(t *testing.T) {
	dispatch := controlDispatcher(&controlServer{})
	probe := func(t *testing.T, protocol string, address net.Addr) *nodegrpc.ListenerCheckReply {
		t.Helper()
		addrPort := netip.MustParseAddrPort(address.String())
		payload, err := proto.Marshal(&nodegrpc.ListenerCheckRequest{
			Protocol: protocol, Ip: addrPort.Addr().String(), Port: uint32(addrPort.Port()),
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := dispatch(t.Context(), nodegrpc.V1_CheckListener_FullMethodName, payload)
		if err != nil {
			t.Fatal(err)
		}
		reply := &nodegrpc.ListenerCheckReply{}
		if err := proto.Unmarshal(result, reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	t.Run("TCP", func(t *testing.T) {
		socket, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer socket.Close()
		address := socket.Addr()
		if reply := probe(t, "tcp", address); reply.GetAvailable() || reply.GetReason() != "occupied" {
			t.Fatalf("TCP socket in use = %v", reply)
		}
		if err := socket.Close(); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if reply := probe(t, "tcp", address); !reply.GetAvailable() || reply.GetReason() != "" {
				t.Fatalf("TCP socket after close = %v", reply)
			}
		}
	})
	t.Run("UDP", func(t *testing.T) {
		socket, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer socket.Close()
		address := socket.LocalAddr()
		if reply := probe(t, "udp", address); reply.GetAvailable() || reply.GetReason() != "occupied" {
			t.Fatalf("UDP socket in use = %v", reply)
		}
		if err := socket.Close(); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if reply := probe(t, "udp", address); !reply.GetAvailable() || reply.GetReason() != "" {
				t.Fatalf("UDP socket after close = %v", reply)
			}
		}
	})
}

func TestCheckListenerRejectsInvalidBindings(t *testing.T) {
	control := &controlServer{}
	for _, request := range []*nodegrpc.ListenerCheckRequest{
		nil,
		{Protocol: "http", Port: 18080},
		{Protocol: "tcp"},
		{Protocol: "udp", Port: 65536},
		{Protocol: "tcp", Ip: "example.test", Port: 18080},
		{Protocol: "tcp", Ip: "fe80::1%eth0", Port: 18080},
	} {
		if _, err := control.CheckListener(context.Background(), request); status.Code(err) != codes.InvalidArgument {
			t.Errorf("invalid check %v = %v", request, err)
		}
	}
	reply, err := control.CheckListener(t.Context(), &nodegrpc.ListenerCheckRequest{
		Protocol: "tcp", Ip: "192.0.2.41", Port: 18080,
	})
	if err != nil || reply.GetAvailable() || reply.GetReason() != "address_unavailable" {
		t.Fatalf("address absent from local interfaces = %v, %v", reply, err)
	}
}
