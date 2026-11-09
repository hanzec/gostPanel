package gost

import (
	"sync"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nodeStream struct {
	generation uint64
	username   string
	password   string
	session    *nodegrpc.Session
}

var nodeStreams = struct {
	sync.Mutex
	byID map[uint]nodeStream
}{byID: make(map[uint]nodeStream)}

// RegisterNodeStream replaces the previous connection unless it has a newer enrollment.
// A rejected stream returns nil and remains the caller's responsibility to close.
func RegisterNodeStream(id uint, generation uint64, username, password string, session *nodegrpc.Session) func() {
	nodeStreams.Lock()
	previous := nodeStreams.byID[id]
	if previous.generation > generation {
		nodeStreams.Unlock()
		return nil
	}
	nodeStreams.byID[id] = nodeStream{generation: generation, username: username, password: password, session: session}
	nodeStreams.Unlock()
	if previous.session != nil {
		previous.session.Close()
	}
	return func() {
		nodeStreams.Lock()
		defer nodeStreams.Unlock()
		if nodeStreams.byID[id].session == session {
			delete(nodeStreams.byID, id)
		}
	}
}

// DisconnectNode closes a session after its registration is removed or replaced.
func DisconnectNode(id uint) {
	nodeStreams.Lock()
	session := nodeStreams.byID[id].session
	delete(nodeStreams.byID, id)
	nodeStreams.Unlock()
	if session != nil {
		session.Close()
	}
}

func (c *Client) connect() (nodegrpc.V1Client, error) {
	if c.config.NodeID == 0 || c.config.Username == "" || c.config.Password == "" {
		return nil, status.Error(codes.FailedPrecondition, "节点尚未通过令牌注册")
	}
	nodeStreams.Lock()
	current := nodeStreams.byID[c.config.NodeID]
	nodeStreams.Unlock()
	if current.session == nil || current.username != c.config.Username || current.password != c.config.Password {
		return nil, status.Error(codes.Unavailable, "节点尚未连接面板")
	}
	return nodegrpc.NewV1Client(current.session), nil
}
