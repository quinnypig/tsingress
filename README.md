# tsingress

A single-binary reverse proxy that terminates TLS and forwards traffic to services on your [Tailscale](https://tailscale.com) network.

```
Internet ──HTTPS:443──▶ tsingress ──tsnet──▶ tailnet ──▶ your service
```

## Why

Tailscale Funnel doesn't support custom domains. If you want `billing.example.com` to reach a service on your tailnet, you need a reverse proxy with a public IP. nginx and Caddy work, but neither understands Tailscale topology. tsingress does exactly one thing: map public domains to tailnet backends with automatic TLS.

## Features

- **Minimal config** — one domain = one line of YAML
- **Automatic TLS** — Let's Encrypt certificates obtained and renewed via `autocert`
- **Tailscale-native** — embeds a Tailscale node via `tsnet`; no separate Tailscale client needed
- **Health checks** — distinguishes "service is down" (503) from "tailnet connectivity lost" (502)
- **SIGHUP reload** — add or remove routes without restarting
- **Single binary** — no sidecars, no containers required

## Quick Start

1. **Create a config file:**

```yaml
# /etc/tsingress/tsingress.yaml
tailscale:
  authkey: "tskey-auth-xxxxx"   # or set TS_AUTHKEY env var
  hostname: "ingress"

acme:
  email: "ops@example.com"

routes:
  - domain: billing.example.com
    backend: billing-server:8080

  - domain: grafana.example.com
    backend: grafana.tail-scale.ts.net:3000
    health_check:
      path: /api/health
      interval: 30s

  - domain: wiki.example.com
    backend: 100.64.0.5:8080
    headers:
      X-Forwarded-Host: wiki.example.com
```

2. **Point your DNS** — A/AAAA records for each domain pointing at the host running tsingress.

3. **Run it:**

```bash
tsingress -config /etc/tsingress/tsingress.yaml
```

That's it. TLS happens. Certs renew. Health checks run.

## Installation

### From releases

Download the latest binary from [GitHub Releases](https://github.com/quinnypig/tsingress/releases) for your platform.

### From source

```bash
go install github.com/quinnypig/tsingress@latest
```

### Docker

```bash
docker run -v /etc/tsingress:/etc/tsingress \
  -p 443:443 -p 80:80 \
  ghcr.io/quinnypig/tsingress:latest
```

## Configuration Reference

### `tailscale`

| Field | Default | Description |
|---|---|---|
| `authkey` | `$TS_AUTHKEY` | Tailscale auth key (or OAuth client secret) |
| `hostname` | `tsingress` | Node name in your tailnet |
| `state_dir` | `/var/lib/tsingress/tsnet` | tsnet state directory |

### `acme`

| Field | Default | Description |
|---|---|---|
| `email` | *(none)* | Let's Encrypt registration email |
| `cert_dir` | `/var/lib/tsingress/certs` | Certificate cache directory |

### `routes`

| Field | Required | Description |
|---|---|---|
| `domain` | yes | Public domain name |
| `backend` | yes | Tailnet address (`host:port`, MagicDNS name, or Tailscale IP) |
| `headers` | no | Extra headers to set on proxied requests |
| `health_check.path` | no | HTTP path to probe (default: `/`) |
| `health_check.interval` | no | Probe interval (default: `30s`) |

## Deployment

### systemd

```ini
# /etc/systemd/system/tsingress.service
[Unit]
Description=tsingress - Tailscale ingress proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/tsingress -config /etc/tsingress/tsingress.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5
User=tsingress
Group=tsingress
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/tsingress

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now tsingress
```

### Reload config without downtime

```bash
# Edit /etc/tsingress/tsingress.yaml, then:
sudo systemctl reload tsingress
```

## Health Checking

tsingress distinguishes three backend states:

| State | HTTP Status | Meaning |
|---|---|---|
| **Healthy** | proxied normally | Backend responded to health probe |
| **Unhealthy** | 503 | Backend didn't respond (service down, tailnet fine) |
| **Disconnected** | 502 | Can't reach the tailnet node at all |

The `/-/health` endpoint on the public interface returns 200 if at least one backend is healthy.

## Security

tsingress is designed to sit on the public internet with a minimal attack surface:

- TLS termination via Go's `crypto/tls`
- HTTP parsing via Go's `net/http`
- ACME challenges restricted to configured domains only
- All backend traffic encrypted over WireGuard (tailnet)
- Run as non-root with `CAP_NET_BIND_SERVICE`

## Architecture

```
┌──────────────────────────────────────┐
│            tsingress                  │
│                                      │
│  ┌─────────┐    ┌──────────────┐    │
│  │  ACME/  │    │  Tailscale   │    │
│  │  TLS    │    │  (tsnet)     │────┼──▶ tailnet
│  │  termn  │    │              │    │
│  └────┬────┘    └──────┬───────┘    │
│       │               │            │
│       ▼               ▼            │
│   ┌─────────────────────┐          │
│   │   reverse proxy     │          │
│   │   (per-domain)      │          │
│   └─────────────────────┘          │
└──────────────────────────────────────┘
```

### Dependencies

| Dependency | Purpose |
|---|---|
| `tailscale.com/tsnet` | Embedded Tailscale node |
| `golang.org/x/crypto/acme/autocert` | Let's Encrypt certificate management |
| `gopkg.in/yaml.v3` | Config parsing |
| `net/http`, `net/http/httputil` | HTTP server and reverse proxy (stdlib) |

## License

MIT
