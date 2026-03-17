package tls

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewManager_DomainSetup(t *testing.T) {
	domains := []string{"example.com", "foo.example.com"}
	m := NewManager("test@example.com", t.TempDir(), domains, testLogger())

	if m == nil {
		t.Fatal("NewManager returned nil")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, d := range domains {
		if !m.domains[d] {
			t.Errorf("domain %q not found in manager domains", d)
		}
	}
	if len(m.domains) != len(domains) {
		t.Errorf("expected %d domains, got %d", len(domains), len(m.domains))
	}
}

func TestHostPolicy_AcceptsConfiguredDomains(t *testing.T) {
	domains := []string{"allowed.com", "also-allowed.com"}
	m := NewManager("test@example.com", t.TempDir(), domains, testLogger())

	for _, d := range domains {
		if err := m.hostPolicy(context.Background(), d); err != nil {
			t.Errorf("hostPolicy rejected configured domain %q: %v", d, err)
		}
	}
}

func TestHostPolicy_RejectsUnconfiguredDomains(t *testing.T) {
	m := NewManager("test@example.com", t.TempDir(), []string{"allowed.com"}, testLogger())

	rejected := []string{"evil.com", "not-allowed.com", ""}
	for _, d := range rejected {
		if err := m.hostPolicy(context.Background(), d); err == nil {
			t.Errorf("hostPolicy should have rejected unconfigured domain %q", d)
		}
	}
}

func TestSetDomains_UpdatesAllowedSet(t *testing.T) {
	m := NewManager("test@example.com", t.TempDir(), []string{"old.com"}, testLogger())

	m.SetDomains([]string{"new.com", "also-new.com"})

	if err := m.hostPolicy(context.Background(), "new.com"); err != nil {
		t.Errorf("hostPolicy rejected newly added domain: %v", err)
	}
	if err := m.hostPolicy(context.Background(), "also-new.com"); err != nil {
		t.Errorf("hostPolicy rejected newly added domain: %v", err)
	}
}

func TestSetDomains_ReplacesNotAppends(t *testing.T) {
	m := NewManager("test@example.com", t.TempDir(), []string{"original.com"}, testLogger())

	m.SetDomains([]string{"replacement.com"})

	if err := m.hostPolicy(context.Background(), "original.com"); err == nil {
		t.Error("hostPolicy should have rejected original domain after SetDomains replaced the set")
	}
	if err := m.hostPolicy(context.Background(), "replacement.com"); err != nil {
		t.Errorf("hostPolicy rejected replacement domain: %v", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.domains) != 1 {
		t.Errorf("expected 1 domain after replacement, got %d", len(m.domains))
	}
}

func TestTLSConfig_ReturnsNonNil(t *testing.T) {
	m := NewManager("test@example.com", t.TempDir(), []string{"example.com"}, testLogger())

	cfg := m.TLSConfig()
	if cfg == nil {
		t.Fatal("TLSConfig returned nil")
	}
}

func TestHTTPHandler_ReturnsNonNil(t *testing.T) {
	m := NewManager("test@example.com", t.TempDir(), []string{"example.com"}, testLogger())

	h := m.HTTPHandler(nil)
	if h == nil {
		t.Fatal("HTTPHandler returned nil")
	}
}

func TestHTTPHandler_FallbackForNonACME(t *testing.T) {
	m := NewManager("test@example.com", t.TempDir(), []string{"example.com"}, testLogger())

	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fallback"))
	})

	handler := m.HTTPHandler(fallback)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/hello", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 from fallback, got %d", rec.Code)
	}
	if rec.Body.String() != "fallback" {
		t.Errorf("expected body %q, got %q", "fallback", rec.Body.String())
	}
}
