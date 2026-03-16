package config

import (
	"testing"
)

func TestParseValidConfig(t *testing.T) {
	yaml := []byte(`
tailscale:
  authkey: "tskey-auth-test"
  hostname: "ingress"
routes:
  - domain: billing.example.com
    backend: billing-server:8080
  - domain: grafana.example.com
    backend: grafana:3000
    health_check:
      path: /api/health
      interval: 30s
`)
	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(cfg.Routes))
	}
	if cfg.Routes[0].Domain != "billing.example.com" {
		t.Errorf("expected domain billing.example.com, got %s", cfg.Routes[0].Domain)
	}
	if cfg.Routes[1].HealthCheck == nil {
		t.Fatal("expected health check on second route")
	}
	if cfg.Routes[1].HealthCheck.Path != "/api/health" {
		t.Errorf("expected health check path /api/health, got %s", cfg.Routes[1].HealthCheck.Path)
	}
	if cfg.Tailscale.Hostname != "ingress" {
		t.Errorf("expected hostname ingress, got %s", cfg.Tailscale.Hostname)
	}
	// Check defaults
	if cfg.ACME.CertDir != "/var/lib/tsingress/certs" {
		t.Errorf("expected default cert dir, got %s", cfg.ACME.CertDir)
	}
}

func TestParseNoRoutes(t *testing.T) {
	yaml := []byte(`
tailscale:
  authkey: "tskey-auth-test"
routes: []
`)
	_, err := Parse(yaml)
	if err == nil {
		t.Fatal("expected error for empty routes")
	}
}

func TestParseDuplicateDomain(t *testing.T) {
	yaml := []byte(`
routes:
  - domain: billing.example.com
    backend: a:8080
  - domain: billing.example.com
    backend: b:8080
`)
	_, err := Parse(yaml)
	if err == nil {
		t.Fatal("expected error for duplicate domain")
	}
}

func TestParseMissingBackend(t *testing.T) {
	yaml := []byte(`
routes:
  - domain: billing.example.com
`)
	_, err := Parse(yaml)
	if err == nil {
		t.Fatal("expected error for missing backend")
	}
}

func TestRouteByDomain(t *testing.T) {
	cfg := &Config{
		Routes: []Route{
			{Domain: "a.example.com", Backend: "a:80"},
			{Domain: "b.example.com", Backend: "b:80"},
		},
	}
	r := cfg.RouteByDomain("a.example.com")
	if r == nil || r.Backend != "a:80" {
		t.Error("RouteByDomain failed for existing domain")
	}
	if cfg.RouteByDomain("c.example.com") != nil {
		t.Error("RouteByDomain should return nil for unknown domain")
	}
}

func TestDomains(t *testing.T) {
	cfg := &Config{
		Routes: []Route{
			{Domain: "a.example.com", Backend: "a:80"},
			{Domain: "b.example.com", Backend: "b:80"},
		},
	}
	domains := cfg.Domains()
	if len(domains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(domains))
	}
}
