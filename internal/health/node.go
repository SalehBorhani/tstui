package health

import (
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// NodeState is a node's reachability as far as `tsh ls` can tell. tstui
// never opens a connection just to check a node.
type NodeState int

// Node states.
const (
	// NodeUnknown: tsh gave no heartbeat expiry.
	NodeUnknown NodeState = iota
	// NodeOnline: the node's heartbeat was current when it was listed.
	NodeOnline
	// NodeStale: the node was listed with a heartbeat that had already run
	// out, so it has probably gone away (the cluster drops such nodes
	// shortly after).
	NodeStale
)

// NodeStatus classifies srv, listed at listedAt. Teleport lists a node
// while its heartbeat (metadata.expires) is in the future; nodes renew it
// every few minutes while they are connected.
func NodeStatus(srv tsh.Server, listedAt time.Time) NodeState {
	switch {
	case srv.Expires.IsZero():
		return NodeUnknown
	case srv.Expires.Before(listedAt):
		return NodeStale
	}
	return NodeOnline
}
