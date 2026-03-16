// Package proxy builds per-route reverse proxies that dial through the tailnet.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/quinnypig/tsingress/config"
)

// Router dispatches incoming requests to the correct backend reverse proxy based on Host header.
type Router struct {
	mu       sync.RWMutex
	proxies  map[string]*httputil.ReverseProxy
	routes   map[string]*config.Route
	logger   *slog.Logger
	transport http.RoundTripper

	// HealthFunc is called to check whether a backend is healthy before proxying.
	// Returns "healthy", "unhealthy", or "disconnected".
	HealthFunc func(domain string) string
}

// NewRouter creates a Router that dispatches to backends via the given transport.
func NewRouter(transport http.RoundTripper, logger *slog.Logger) *Router {
	return &Router{
		proxies:   make(map[string]*httputil.ReverseProxy),
		routes:    make(map[string]*config.Route),
		transport: transport,
		logger:    logger,
	}
}

// SetRoutes replaces the current route table.
func (r *Router) SetRoutes(routes []config.Route) {
	proxies := make(map[string]*httputil.ReverseProxy, len(routes))
	routeMap := make(map[string]*config.Route, len(routes))
	for i := range routes {
		route := &routes[i]
		proxies[route.Domain] = r.buildProxy(route)
		routeMap[route.Domain] = route
	}
	r.mu.Lock()
	r.proxies = proxies
	r.routes = routeMap
	r.mu.Unlock()
	r.logger.Info("routes updated", "count", len(routes))
}

func (r *Router) buildProxy(route *config.Route) *httputil.ReverseProxy {
	backend := route.Backend
	// Ensure the backend URL has a scheme.
	if !strings.Contains(backend, "://") {
		backend = "http://" + backend
	}
	target, _ := url.Parse(backend)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = target.Host

			// Apply custom headers from config.
			for k, v := range route.Headers {
				pr.Out.Header.Set(k, v)
			}
		},
		Transport: r.transport,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			r.logger.Error("proxy error",
				"domain", route.Domain,
				"backend", route.Backend,
				"error", err,
			)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	return proxy
}

// ServeHTTP dispatches the request to the appropriate backend.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host := stripPort(req.Host)
	start := time.Now()

	r.mu.RLock()
	proxy := r.proxies[host]
	r.mu.RUnlock()

	if proxy == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Check backend health if a health function is registered.
	if r.HealthFunc != nil {
		switch r.HealthFunc(host) {
		case "unhealthy":
			r.logger.Warn("backend unhealthy, returning 503", "domain", host)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		case "disconnected":
			r.logger.Warn("tailnet disconnected from backend, returning 502", "domain", host)
			http.Error(w, "Bad Gateway: tailnet connectivity lost to backend", http.StatusBadGateway)
			return
		}
	}

	proxy.ServeHTTP(w, req)

	r.logger.Info("request",
		"domain", host,
		"method", req.Method,
		"path", req.URL.Path,
		"duration", time.Since(start),
	)
}

// HealthHandler returns 200 if at least one backend is healthy.
func (r *Router) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.HealthFunc != nil {
			r.mu.RLock()
			domains := make([]string, 0, len(r.routes))
			for d := range r.routes {
				domains = append(domains, d)
			}
			r.mu.RUnlock()

			for _, d := range domains {
				if r.HealthFunc(d) == "healthy" {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("OK\n"))
					return
				}
			}
			http.Error(w, "No healthy backends", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
	}
}

func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}
