// Package config handles YAML configuration parsing and validation for tsingress.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level tsingress configuration.
type Config struct {
	Tailscale TailscaleConfig `yaml:"tailscale"`
	ACME      ACMEConfig      `yaml:"acme"`
	Routes    []Route         `yaml:"routes"`
}

// TailscaleConfig controls the embedded tsnet node.
type TailscaleConfig struct {
	AuthKey  string `yaml:"authkey"`
	Hostname string `yaml:"hostname"`
	StateDir string `yaml:"state_dir"`
}

// ACMEConfig controls Let's Encrypt certificate management.
type ACMEConfig struct {
	Email   string `yaml:"email"`
	CertDir string `yaml:"cert_dir"`
}

// Route maps a public domain to a tailnet backend.
type Route struct {
	Domain      string            `yaml:"domain"`
	Backend     string            `yaml:"backend"`
	Headers     map[string]string `yaml:"headers"`
	HealthCheck *HealthCheck      `yaml:"health_check"`
}

// HealthCheck configures periodic probing of a backend.
type HealthCheck struct {
	Path     string        `yaml:"path"`
	Interval time.Duration `yaml:"interval"`
}

// Load reads and parses a tsingress YAML config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return Parse(data)
}

// Parse parses raw YAML bytes into a Config, applying defaults and validation.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Tailscale.Hostname == "" {
		cfg.Tailscale.Hostname = "tsingress"
	}
	if cfg.Tailscale.StateDir == "" {
		cfg.Tailscale.StateDir = "/var/lib/tsingress/tsnet"
	}
	if cfg.ACME.CertDir == "" {
		cfg.ACME.CertDir = "/var/lib/tsingress/certs"
	}
	if cfg.Tailscale.AuthKey == "" {
		cfg.Tailscale.AuthKey = os.Getenv("TS_AUTHKEY")
	}
	for i := range cfg.Routes {
		if cfg.Routes[i].HealthCheck != nil && cfg.Routes[i].HealthCheck.Interval == 0 {
			cfg.Routes[i].HealthCheck.Interval = 30 * time.Second
		}
		if cfg.Routes[i].HealthCheck != nil && cfg.Routes[i].HealthCheck.Path == "" {
			cfg.Routes[i].HealthCheck.Path = "/"
		}
	}
}

func validate(cfg *Config) error {
	if len(cfg.Routes) == 0 {
		return fmt.Errorf("config: at least one route is required")
	}
	seen := make(map[string]bool)
	for i, r := range cfg.Routes {
		if r.Domain == "" {
			return fmt.Errorf("config: route[%d] missing domain", i)
		}
		if r.Backend == "" {
			return fmt.Errorf("config: route[%d] (%s) missing backend", i, r.Domain)
		}
		if seen[r.Domain] {
			return fmt.Errorf("config: duplicate domain %q", r.Domain)
		}
		seen[r.Domain] = true
	}
	return nil
}

// Domains returns all configured domain names.
func (c *Config) Domains() []string {
	domains := make([]string, len(c.Routes))
	for i, r := range c.Routes {
		domains[i] = r.Domain
	}
	return domains
}

// RouteByDomain returns the route for a given domain, or nil if not found.
func (c *Config) RouteByDomain(domain string) *Route {
	for i := range c.Routes {
		if c.Routes[i].Domain == domain {
			return &c.Routes[i]
		}
	}
	return nil
}
