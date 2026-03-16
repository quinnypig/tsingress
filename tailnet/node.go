// Package tailnet manages the embedded tsnet.Server lifecycle.
package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"

	"tailscale.com/tsnet"
)

// Node wraps a tsnet.Server and tracks connectivity state.
type Node struct {
	server    *tsnet.Server
	connected atomic.Bool
	logger    *slog.Logger
}

// NewNode creates a new embedded Tailscale node.
func NewNode(hostname, authKey, stateDir string, logger *slog.Logger) *Node {
	s := &tsnet.Server{
		Hostname: hostname,
		AuthKey:  authKey,
		Dir:      stateDir,
	}
	return &Node{
		server: s,
		logger: logger,
	}
}

// Start brings the tsnet node online and blocks until it's ready.
func (n *Node) Start(ctx context.Context) error {
	n.logger.Info("starting tailscale node", "hostname", n.server.Hostname)
	if _, err := n.server.Up(ctx); err != nil {
		return fmt.Errorf("tsnet start: %w", err)
	}
	n.connected.Store(true)
	n.logger.Info("tailscale node is online")
	return nil
}

// Dial connects to a tailnet address through the embedded node.
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.server.Dial(ctx, network, addr)
}

// Connected returns true if the tsnet node is up.
func (n *Node) Connected() bool {
	return n.connected.Load()
}

// Close shuts down the tsnet node.
func (n *Node) Close() error {
	n.connected.Store(false)
	return n.server.Close()
}
