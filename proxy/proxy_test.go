package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quinnypig/tsingress/config"
)

// discardLogger returns a logger that writes to nowhere.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestRouter creates a Router using http.DefaultTransport (for talking to httptest servers on localhost).
func newTestRouter() *Router {
	return NewRouter(http.DefaultTransport, discardLogger())
}

// --- ServeHTTP tests ---

func TestServeHTTP_UnknownHost_Returns404(t *testing.T) {
	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "known.example.com", Backend: "http://127.0.0.1:9999"},
	})

	req := httptest.NewRequest(http.MethodGet, "http://unknown.example.com/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestServeHTTP_ProxiesToCorrectBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Marker", "reached-backend")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "backend-response")
	}))
	defer backend.Close()

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "app.example.com", Backend: backend.URL},
	})

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com/hello", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body != "backend-response" {
		t.Errorf("expected body %q, got %q", "backend-response", body)
	}
	if rec.Header().Get("X-Test-Marker") != "reached-backend" {
		t.Errorf("expected X-Test-Marker header from backend")
	}
}

func TestServeHTTP_HostWithPort_StripsPort(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer backend.Close()

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "app.example.com", Backend: backend.URL},
	})

	req := httptest.NewRequest(http.MethodGet, "http://app.example.com:8443/", nil)
	req.Host = "app.example.com:8443"
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 when host has port, got %d", rec.Code)
	}
}

func TestServeHTTP_Unhealthy_Returns503(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "should not reach")
	}))
	defer backend.Close()

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "sick.example.com", Backend: backend.URL},
	})
	router.HealthFunc = func(domain string) string {
		return "unhealthy"
	}

	req := httptest.NewRequest(http.MethodGet, "http://sick.example.com/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
}

func TestServeHTTP_Disconnected_Returns502(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "should not reach")
	}))
	defer backend.Close()

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "lost.example.com", Backend: backend.URL},
	})
	router.HealthFunc = func(domain string) string {
		return "disconnected"
	}

	req := httptest.NewRequest(http.MethodGet, "http://lost.example.com/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "tailnet connectivity lost") {
		t.Errorf("expected tailnet connectivity message in body, got %q", body)
	}
}

func TestServeHTTP_HealthyBackend_Proxies(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "healthy-response")
	}))
	defer backend.Close()

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "good.example.com", Backend: backend.URL},
	})
	router.HealthFunc = func(domain string) string {
		return "healthy"
	}

	req := httptest.NewRequest(http.MethodGet, "http://good.example.com/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "healthy-response" {
		t.Errorf("expected healthy-response, got %q", rec.Body.String())
	}
}

// --- HealthHandler tests ---

func TestHealthHandler_AtLeastOneHealthy_Returns200(t *testing.T) {
	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "a.example.com", Backend: "http://127.0.0.1:1"},
		{Domain: "b.example.com", Backend: "http://127.0.0.1:2"},
	})
	router.HealthFunc = func(domain string) string {
		if domain == "a.example.com" {
			return "unhealthy"
		}
		return "healthy"
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.HealthHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OK") {
		t.Errorf("expected OK body, got %q", rec.Body.String())
	}
}

func TestHealthHandler_NoHealthyBackends_Returns503(t *testing.T) {
	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "a.example.com", Backend: "http://127.0.0.1:1"},
		{Domain: "b.example.com", Backend: "http://127.0.0.1:2"},
	})
	router.HealthFunc = func(domain string) string {
		return "unhealthy"
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.HealthHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rec.Code)
	}
}

func TestHealthHandler_NoHealthFunc_Returns200(t *testing.T) {
	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "a.example.com", Backend: "http://127.0.0.1:1"},
	})
	// HealthFunc is nil by default

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	router.HealthHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OK") {
		t.Errorf("expected OK body, got %q", rec.Body.String())
	}
}

// --- SetRoutes atomicity test ---

func TestSetRoutes_AtomicSwap(t *testing.T) {
	router := newTestRouter()

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "backend-a")
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "backend-b")
	}))
	defer backendB.Close()

	// Set initial routes with domain alpha pointing to backendA.
	router.SetRoutes([]config.Route{
		{Domain: "alpha.example.com", Backend: backendA.URL},
	})

	// Verify alpha routes to backendA.
	req := httptest.NewRequest(http.MethodGet, "http://alpha.example.com/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Body.String() != "backend-a" {
		t.Fatalf("expected backend-a before swap, got %q", rec.Body.String())
	}

	// Swap: replace with beta pointing to backendB (alpha should vanish).
	router.SetRoutes([]config.Route{
		{Domain: "beta.example.com", Backend: backendB.URL},
	})

	// alpha should now 404.
	req = httptest.NewRequest(http.MethodGet, "http://alpha.example.com/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for removed route, got %d", rec.Code)
	}

	// beta should proxy to backendB.
	req = httptest.NewRequest(http.MethodGet, "http://beta.example.com/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Body.String() != "backend-b" {
		t.Errorf("expected backend-b after swap, got %q", rec.Body.String())
	}
}

func TestSetRoutes_ConcurrentAccess(t *testing.T) {
	router := newTestRouter()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer backend.Close()

	router.SetRoutes([]config.Route{
		{Domain: "concurrent.example.com", Backend: backend.URL},
	})

	var wg sync.WaitGroup
	// Run concurrent readers and writers to exercise the RWMutex.
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://concurrent.example.com/", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
		}()
		go func() {
			defer wg.Done()
			router.SetRoutes([]config.Route{
				{Domain: "concurrent.example.com", Backend: backend.URL},
			})
		}()
	}
	wg.Wait()
}

// --- Custom headers test ---

func TestServeHTTP_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer backend.Close()

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{
			Domain:  "headers.example.com",
			Backend: backend.URL,
			Headers: map[string]string{
				"X-Custom-Auth":  "secret-token",
				"X-Forwarded-By": "tsingress",
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "http://headers.example.com/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if receivedHeaders.Get("X-Custom-Auth") != "secret-token" {
		t.Errorf("expected X-Custom-Auth=secret-token, got %q", receivedHeaders.Get("X-Custom-Auth"))
	}
	if receivedHeaders.Get("X-Forwarded-By") != "tsingress" {
		t.Errorf("expected X-Forwarded-By=tsingress, got %q", receivedHeaders.Get("X-Forwarded-By"))
	}
}

// --- stripPort tests ---

func TestStripPort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"example.com:443", "example.com"},
		{"example.com:8080", "example.com"},
		{"example.com", "example.com"},
		{"[::1]:443", "[::1]"},
		{"localhost:80", "localhost"},
		{"no-port.example.com", "no-port.example.com"},
		{":8080", ""},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := stripPort(tc.input)
			if got != tc.want {
				t.Errorf("stripPort(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- NewTailnetTransport tests ---

func TestNewTailnetTransport_ReturnsValidTransport(t *testing.T) {
	dialCalled := false
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCalled = true
		return nil, fmt.Errorf("test dial: not implemented")
	}

	rt := NewTailnetTransport(dial)
	if rt == nil {
		t.Fatal("NewTailnetTransport returned nil")
	}

	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}

	if transport.MaxIdleConns != 100 {
		t.Errorf("expected MaxIdleConns=100, got %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 10 {
		t.Errorf("expected MaxIdleConnsPerHost=10, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("expected IdleConnTimeout=90s, got %s", transport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("expected TLSHandshakeTimeout=10s, got %s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("expected ResponseHeaderTimeout=30s, got %s", transport.ResponseHeaderTimeout)
	}

	// Verify the dialer is wired up by attempting a request.
	req := httptest.NewRequest(http.MethodGet, "http://fake.backend:80/", nil)
	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error from test dialer")
	}
	if !dialCalled {
		t.Error("expected custom dialer to be called")
	}
}

// --- Backend scheme handling test ---

func TestBuildProxy_AddsHTTPScheme(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "scheme-ok")
	}))
	defer backend.Close()

	// Strip the http:// scheme to test automatic scheme addition.
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	router := newTestRouter()
	router.SetRoutes([]config.Route{
		{Domain: "noscheme.example.com", Backend: backendAddr},
	})

	req := httptest.NewRequest(http.MethodGet, "http://noscheme.example.com/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "scheme-ok" {
		t.Errorf("expected body scheme-ok, got %q", rec.Body.String())
	}
}

// --- NewRouter test ---

func TestNewRouter_InitializesEmptyMaps(t *testing.T) {
	router := NewRouter(http.DefaultTransport, discardLogger())

	// With no routes, every host should 404.
	req := httptest.NewRequest(http.MethodGet, "http://anything.example.com/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for empty router, got %d", rec.Code)
	}
}
