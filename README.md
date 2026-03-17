# tsingress

A single-binary reverse proxy that terminates TLS and forwards traffic to services on your [Tailscale](https://tailscale.com) network.

```
Internet ──HTTPS:443──▶ tsingress ──tsnet──▶ tailnet ──▶ your service
```

## The Problem

I love Tailscale. I tell everyone I love Tailscale. I have said publicly, on stages, in front of hundreds of people, that Tailscale is one of the best pieces of software I've ever used. I mean it.

But Tailscale Funnel doesn't support custom domains.

If you want `billing.example.com` to reach a service running on your tailnet, your options are: stand up nginx (which has never heard of your tailnet and does not care about your feelings), use Caddy (which is lovely but still doesn't understand Tailscale topology), or do what I did—spend a weekend writing a single-purpose reverse proxy that embeds a Tailscale node and does exactly one thing well.

tsingress is the third option. One binary. One config file. One domain per line. It handles TLS, it handles health checks, and it shuts up about everything else.

## What It Does

- **One domain = one line of YAML.** If your config is longer than your service list, something has gone wrong with your life.
- **Automatic TLS** — Let's Encrypt certificates obtained and renewed via `autocert`. You don't have to think about it. You don't *get* to think about it.
- **Tailscale-native** — Embeds a Tailscale node via `tsnet`. No sidecar. No separate Tailscale client. No "install tailscale on the host and then also install this other thing and then wire them together with hope and YAML."
- **Three-state health checks** — Distinguishes "your service is down" (503) from "tailnet connectivity is gone" (502). Because when your pager goes off at 3am, you want to know if the problem is your code or your network. (It's your code. It's always your code.)
- **SIGHUP reload** — Add or remove routes without restarting. Life's too short for `systemctl restart`.
- **Single binary** — No sidecars, no containers required, no microservices-of-microservices. Just a binary that runs and does the thing.

## Quick Start

**1. Write the config:**

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

**2. Point DNS.** A/AAAA records for each domain at the host running tsingress.

**3. Run it:**

```bash
tsingress -config /etc/tsingress/tsingress.yaml
```

That's it. TLS happens. Certs renew. Health checks run. You can go do something else now.

## Installation

### From releases

Download the latest binary from [GitHub Releases](https://github.com/quinnypig/tsingress/releases). Builds exist for linux and macOS on both amd64 and arm64, because I'm not an animal.

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

## Configuration

### `tailscale`

| Field | Default | Description |
|---|---|---|
| `authkey` | `$TS_AUTHKEY` | Tailscale auth key (or OAuth client secret) |
| `hostname` | `tsingress` | Node name in your tailnet |
| `state_dir` | `/var/lib/tsingress/tsnet` | tsnet state directory |

### `acme`

| Field | Default | Description |
|---|---|---|
| `email` | *(required)* | Let's Encrypt registration email |
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
sudo systemctl reload tsingress
```

## Health Checking

tsingress doesn't just proxy blindly. It distinguishes three backend states, because "it's down" is not a diagnosis:

| State | HTTP Status | What It Means |
|---|---|---|
| **Healthy** | proxied normally | Backend responded to the health probe. Everything is fine. Go back to sleep. |
| **Unhealthy** | 503 | Backend didn't respond, but the tailnet connection is fine. Your service is the problem. |
| **Disconnected** | 502 | Can't reach the tailnet node at all. This is a network problem. Or your tailnet is haunted. |

The `/-/health` endpoint returns 200 if at least one backend is healthy.

## Security

tsingress sits on the public internet. The attack surface is small and intentional:

- TLS termination via Go's `crypto/tls`
- HTTP parsing via Go's `net/http`
- ACME challenges restricted to configured domains only (no, you can't trick it into getting a cert for `evil.com`)
- All backend traffic encrypted over WireGuard via the tailnet
- Runs as non-root with `CAP_NET_BIND_SERVICE`

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

Three direct dependencies. That's it. Everything else is transitive from Tailscale's library, and I'm choosing to trust them on that because, again, I love Tailscale and I am showing you this code.

| Dependency | Why |
|---|---|
| `tailscale.com/tsnet` | The whole point. Embedded Tailscale node. |
| `golang.org/x/crypto/acme/autocert` | Let's Encrypt certificate management. |
| `gopkg.in/yaml.v3` | Config parsing. YAML was a mistake but it's the mistake everyone agreed on. |
| `net/http`, `net/http/httputil` | HTTP server and reverse proxy. Stdlib. Free. |

## License

MIT. Do whatever you want. I'm not your lawyer.
