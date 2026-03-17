// Package health implements per-backend health checking over the tailnet.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// State represents the health of a backend.
type State string

const (
	Healthy      State = "healthy"
	Unhealthy    State = "unhealthy"
	Disconnected State = "disconnected"
)

// Dialer dials through the tailnet.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Backend describes a backend to health-check.
type Backend struct {
	Domain   string
	Addr     string // host:port
	Path     string
	Interval time.Duration
}

// Checker runs periodic health probes for backends via the tailnet.
type Checker struct {
	dial   Dialer
	logger *slog.Logger

	mu     sync.RWMutex
	states map[string]State

	cancel context.CancelFunc
}

// NewChecker creates a health checker that dials through the given dialer.
func NewChecker(dial Dialer, logger *slog.Logger) *Checker {
	return &Checker{
		dial:   dial,
		logger: logger,
		states: make(map[string]State),
	}
}

// Start begins health check goroutines for the given backends.
func (c *Checker) Start(backends []Backend) {
	// Stop any previous run.
	c.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	for _, b := range backends {
		c.mu.Lock()
		c.states[b.Domain] = Healthy // optimistic initial state
		c.mu.Unlock()
		go c.loop(ctx, b)
	}
	c.logger.Info("health checks started", "backends", len(backends))
}

// Stop cancels all health check goroutines.
func (c *Checker) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

// State returns the current health state for a domain.
func (c *Checker) GetState(domain string) State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s, ok := c.states[domain]; ok {
		return s
	}
	return Healthy // no health check configured → assume healthy
}

// StateString returns the state as a string, suitable for the proxy's HealthFunc.
func (c *Checker) StateString(domain string) string {
	return string(c.GetState(domain))
}

func (c *Checker) loop(ctx context.Context, b Backend) {
	ticker := time.NewTicker(b.Interval)
	defer ticker.Stop()

	// Run an initial check immediately.
	c.probe(ctx, b)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.probe(ctx, b)
		}
	}
}

func (c *Checker) probe(ctx context.Context, b Backend) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := c.dial(probeCtx, "tcp", b.Addr)
	if err != nil {
		// If we can't even dial, it's a connectivity issue.
		c.setState(b.Domain, Disconnected)
		c.logger.Warn("health check dial failed",
			"domain", b.Domain,
			"backend", b.Addr,
			"error", err,
		)
		return
	}

	// We could dial. Now do an HTTP health check.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return conn, nil
			},
		},
		Timeout: 5 * time.Second,
	}

	url := fmt.Sprintf("http://%s%s", b.Addr, b.Path)
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		c.setState(b.Domain, Unhealthy)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		c.setState(b.Domain, Unhealthy)
		c.logger.Warn("health check request failed",
			"domain", b.Domain,
			"error", err,
		)
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		c.setState(b.Domain, Healthy)
	} else {
		c.setState(b.Domain, Unhealthy)
		c.logger.Warn("health check returned unhealthy status",
			"domain", b.Domain,
			"status", resp.StatusCode,
		)
	}
}

func (c *Checker) setState(domain string, state State) {
	c.mu.Lock()
	old := c.states[domain]
	c.states[domain] = state
	c.mu.Unlock()

	if old != state {
		c.logger.Info("backend state changed",
			"domain", domain,
			"old", string(old),
			"new", string(state),
		)
	}
}
