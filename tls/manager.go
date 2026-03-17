// Package tls wraps autocert.Manager for automatic Let's Encrypt certificate management.
package tls

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"golang.org/x/crypto/acme/autocert"
)

// Manager wraps autocert.Manager and allows dynamic updates to the allowed domain set.
type Manager struct {
	acme   *autocert.Manager
	mu     sync.RWMutex
	domains map[string]bool
	logger *slog.Logger
}

// NewManager creates a TLS manager that obtains certs for the given domains.
func NewManager(email, certDir string, domains []string, logger *slog.Logger) *Manager {
	m := &Manager{
		domains: make(map[string]bool),
		logger:  logger,
	}
	for _, d := range domains {
		m.domains[d] = true
	}

	m.acme = &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Email:      email,
		Cache:      autocert.DirCache(certDir),
		HostPolicy: m.hostPolicy,
	}
	return m
}

func (m *Manager) hostPolicy(_ context.Context, host string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.domains[host] {
		m.logger.Warn("rejected certificate request for unknown domain", "domain", host)
		return fmt.Errorf("host %q not configured", host)
	}
	return nil
}

// TLSConfig returns a *tls.Config suitable for an HTTPS listener.
func (m *Manager) TLSConfig() *tls.Config {
	return m.acme.TLSConfig()
}

// HTTPHandler returns an http.Handler for ACME http-01 challenges.
// Pass this as the handler for your :80 listener; fallback handles non-ACME requests.
func (m *Manager) HTTPHandler(fallback http.Handler) http.Handler {
	return m.acme.HTTPHandler(fallback)
}

// SetDomains atomically updates the set of allowed domains.
func (m *Manager) SetDomains(domains []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.domains = make(map[string]bool, len(domains))
	for _, d := range domains {
		m.domains[d] = true
	}
	m.logger.Info("updated allowed domains", "count", len(domains))
}
