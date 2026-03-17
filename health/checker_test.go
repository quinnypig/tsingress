package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDial returns a Dialer that connects to the given listener address via
// plain TCP. This lets us point probes at an httptest.Server without touching
// real DNS or the tailnet.
func fakeDial(addr string) Dialer {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

// failDial returns a Dialer that always fails.
func failDial() Dialer {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("dial refused")
	}
}

func TestNewChecker(t *testing.T) {
	c := NewChecker(failDial(), discardLogger())
	if c == nil {
		t.Fatal("NewChecker returned nil")
	}
	if c.states == nil {
		t.Fatal("states map not initialised")
	}
	if c.dial == nil {
		t.Fatal("dial func not set")
	}
	if c.logger == nil {
		t.Fatal("logger not set")
	}
}

func TestGetState_UnknownDomainReturnsHealthy(t *testing.T) {
	c := NewChecker(failDial(), discardLogger())
	got := c.GetState("never-registered.example.com")
	if got != Healthy {
		t.Fatalf("expected Healthy for unknown domain, got %s", got)
	}
}

func TestStateString(t *testing.T) {
	c := NewChecker(failDial(), discardLogger())

	// Unknown domain should return "healthy".
	if s := c.StateString("unknown.example.com"); s != "healthy" {
		t.Fatalf("expected \"healthy\", got %q", s)
	}

	// Manually inject a state and verify.
	c.mu.Lock()
	c.states["sick.example.com"] = Unhealthy
	c.mu.Unlock()

	if s := c.StateString("sick.example.com"); s != "unhealthy" {
		t.Fatalf("expected \"unhealthy\", got %q", s)
	}
}

func TestProbe_DialFailSetsDisconnected(t *testing.T) {
	c := NewChecker(failDial(), discardLogger())

	// Pre-set to Healthy so we can observe the transition.
	c.mu.Lock()
	c.states["example.com"] = Healthy
	c.mu.Unlock()

	b := Backend{
		Domain:   "example.com",
		Addr:     "127.0.0.1:0",
		Path:     "/healthz",
		Interval: time.Second,
	}

	c.probe(context.Background(), b)

	got := c.GetState("example.com")
	if got != Disconnected {
		t.Fatalf("expected Disconnected after dial failure, got %s", got)
	}
}

func TestProbe_200SetsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(fakeDial(srv.Listener.Addr().String()), discardLogger())

	// Start in Unhealthy state to verify transition.
	c.mu.Lock()
	c.states["good.example.com"] = Unhealthy
	c.mu.Unlock()

	b := Backend{
		Domain:   "good.example.com",
		Addr:     srv.Listener.Addr().String(),
		Path:     "/healthz",
		Interval: time.Second,
	}

	c.probe(context.Background(), b)

	got := c.GetState("good.example.com")
	if got != Healthy {
		t.Fatalf("expected Healthy after 200, got %s", got)
	}
}

func TestProbe_500SetsUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewChecker(fakeDial(srv.Listener.Addr().String()), discardLogger())

	c.mu.Lock()
	c.states["bad.example.com"] = Healthy
	c.mu.Unlock()

	b := Backend{
		Domain:   "bad.example.com",
		Addr:     srv.Listener.Addr().String(),
		Path:     "/healthz",
		Interval: time.Second,
	}

	c.probe(context.Background(), b)

	got := c.GetState("bad.example.com")
	if got != Unhealthy {
		t.Fatalf("expected Unhealthy after 500, got %s", got)
	}
}

func TestProbe_VariousSuccessStatusCodes(t *testing.T) {
	for _, code := range []int{200, 201, 204, 301, 302} {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			// Disable automatic redirect following so 3xx responses are
			// evaluated directly by the probe's status-code check instead
			// of being followed to a potentially missing location.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if code >= 300 && code < 400 {
					// Write the status code directly without the Location
					// header to avoid the default redirect behaviour.
					w.WriteHeader(code)
					return
				}
				w.WriteHeader(code)
			}))
			defer srv.Close()

			dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, srv.Listener.Addr().String())
			}

			c := NewChecker(dial, discardLogger())
			c.mu.Lock()
			c.states["test.example.com"] = Unhealthy
			c.mu.Unlock()

			b := Backend{
				Domain:   "test.example.com",
				Addr:     srv.Listener.Addr().String(),
				Path:     "/",
				Interval: time.Second,
			}

			// The probe's HTTP client follows redirects by default, and a 3xx
			// without a Location header ends up returning the 3xx status to
			// the caller, which the probe considers healthy (200-399).
			c.probe(context.Background(), b)

			got := c.GetState("test.example.com")
			if got != Healthy {
				t.Fatalf("expected Healthy for status %d, got %s", code, got)
			}
		})
	}
}

func TestStartStop_Lifecycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(fakeDial(srv.Listener.Addr().String()), discardLogger())

	backends := []Backend{
		{
			Domain:   "alpha.example.com",
			Addr:     srv.Listener.Addr().String(),
			Path:     "/health",
			Interval: 50 * time.Millisecond,
		},
		{
			Domain:   "beta.example.com",
			Addr:     srv.Listener.Addr().String(),
			Path:     "/health",
			Interval: 50 * time.Millisecond,
		},
	}

	c.Start(backends)

	// Give the initial probes time to run.
	time.Sleep(150 * time.Millisecond)

	if s := c.GetState("alpha.example.com"); s != Healthy {
		t.Fatalf("alpha: expected Healthy, got %s", s)
	}
	if s := c.GetState("beta.example.com"); s != Healthy {
		t.Fatalf("beta: expected Healthy, got %s", s)
	}

	c.Stop()

	// After Stop, calling Stop again should not panic.
	c.Stop()
}

func TestStartStop_DoubleStartStopsPrevious(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(fakeDial(srv.Listener.Addr().String()), discardLogger())

	backends := []Backend{
		{
			Domain:   "first.example.com",
			Addr:     srv.Listener.Addr().String(),
			Path:     "/",
			Interval: 50 * time.Millisecond,
		},
	}

	c.Start(backends)
	time.Sleep(100 * time.Millisecond)

	// Starting again with different backends should not panic.
	c.Start([]Backend{
		{
			Domain:   "second.example.com",
			Addr:     srv.Listener.Addr().String(),
			Path:     "/",
			Interval: 50 * time.Millisecond,
		},
	})
	time.Sleep(100 * time.Millisecond)

	if s := c.GetState("second.example.com"); s != Healthy {
		t.Fatalf("second: expected Healthy, got %s", s)
	}

	c.Stop()
}

func TestStateTransitions(t *testing.T) {
	// We'll cycle a backend through Healthy -> Disconnected -> Healthy ->
	// Unhealthy by swapping dialer and server behaviour between probes.

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()

	domain := "transitional.example.com"
	backend := func(addr string) Backend {
		return Backend{
			Domain:   domain,
			Addr:     addr,
			Path:     "/",
			Interval: time.Second,
		}
	}

	// Phase 1: healthy
	c := NewChecker(fakeDial(healthy.Listener.Addr().String()), discardLogger())
	c.mu.Lock()
	c.states[domain] = Unhealthy // start from non-healthy to verify transition
	c.mu.Unlock()

	c.probe(context.Background(), backend(healthy.Listener.Addr().String()))
	if s := c.GetState(domain); s != Healthy {
		t.Fatalf("phase 1: expected Healthy, got %s", s)
	}

	// Phase 2: dial failure -> Disconnected
	c.dial = failDial()
	c.probe(context.Background(), backend("127.0.0.1:1"))
	if s := c.GetState(domain); s != Disconnected {
		t.Fatalf("phase 2: expected Disconnected, got %s", s)
	}

	// Phase 3: recover to healthy
	c.dial = fakeDial(healthy.Listener.Addr().String())
	c.probe(context.Background(), backend(healthy.Listener.Addr().String()))
	if s := c.GetState(domain); s != Healthy {
		t.Fatalf("phase 3: expected Healthy, got %s", s)
	}

	// Phase 4: server returns 503 -> Unhealthy
	c.dial = fakeDial(unhealthy.Listener.Addr().String())
	c.probe(context.Background(), backend(unhealthy.Listener.Addr().String()))
	if s := c.GetState(domain); s != Unhealthy {
		t.Fatalf("phase 4: expected Unhealthy, got %s", s)
	}
}

func TestProbe_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(fakeDial(srv.Listener.Addr().String()), discardLogger())
	c.mu.Lock()
	c.states["ctx.example.com"] = Healthy
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	b := Backend{
		Domain:   "ctx.example.com",
		Addr:     srv.Listener.Addr().String(),
		Path:     "/",
		Interval: time.Second,
	}

	// Should not panic; state will change due to failed dial/request.
	c.probe(ctx, b)
}

func TestStop_NilCancel(t *testing.T) {
	// A freshly created checker has no cancel func; Stop should not panic.
	c := NewChecker(failDial(), discardLogger())
	c.Stop() // should be a no-op
}
