// tsingress: a single-binary reverse proxy that terminates TLS and forwards traffic
// to services on your Tailscale network.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quinnypig/tsingress/config"
	"github.com/quinnypig/tsingress/health"
	"github.com/quinnypig/tsingress/proxy"
	"github.com/quinnypig/tsingress/tailnet"
	tstls "github.com/quinnypig/tsingress/tls"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/tsingress/tsingress.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("tsingress", version)
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	logger.Info("config loaded", "routes", len(cfg.Routes), "domains", cfg.Domains())

	// Start the embedded Tailscale node.
	node := tailnet.NewNode(cfg.Tailscale.Hostname, cfg.Tailscale.AuthKey, cfg.Tailscale.StateDir, logger)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := node.Start(ctx); err != nil {
		cancel()
		logger.Error("failed to start tailscale node", "error", err)
		os.Exit(1)
	}
	cancel()

	// Set up TLS manager.
	tlsMgr := tstls.NewManager(cfg.ACME.Email, cfg.ACME.CertDir, cfg.Domains(), logger)

	// Build the reverse proxy router with a tsnet transport.
	transport := proxy.NewTailnetTransport(node.Dial)
	router := proxy.NewRouter(transport, logger)

	// Set up health checker.
	checker := health.NewChecker(node.Dial, logger)
	router.HealthFunc = checker.StateString

	// Apply initial routes and start health checks.
	applyRoutes(cfg, router, checker)

	// Build the HTTPS mux with a health endpoint.
	mux := http.NewServeMux()
	mux.Handle("/-/health", router.HealthHandler())
	mux.Handle("/", router)

	// HTTPS server on :443.
	tlsConfig := tlsMgr.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS12
	httpsServer := &http.Server{
		Addr:         ":443",
		Handler:      mux,
		TLSConfig:    tlsConfig,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// HTTP server on :80 for ACME challenges + redirect.
	acmeMgr := tlsMgr.HTTPHandler()
	httpServer := &http.Server{
		Addr: ":80",
		Handler: acmeMgr.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := "https://" + r.Host + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Start servers.
	go func() {
		logger.Info("starting HTTPS server", "addr", httpsServer.Addr)
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTPS server error", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("starting HTTP server", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	// Signal handling: SIGHUP reloads config, SIGTERM/SIGINT shuts down gracefully.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)

	for sig := range sigCh {
		switch sig {
		case syscall.SIGHUP:
			logger.Info("received SIGHUP, reloading config")
			newCfg, err := config.Load(*configPath)
			if err != nil {
				logger.Error("config reload failed, keeping current config", "error", err)
				continue
			}
			cfg = newCfg
			tlsMgr.SetDomains(cfg.Domains())
			applyRoutes(cfg, router, checker)
			logger.Info("config reloaded successfully", "routes", len(cfg.Routes))

		case syscall.SIGTERM, syscall.SIGINT:
			logger.Info("shutting down gracefully")
			checker.Stop()

			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()

			httpsServer.Shutdown(shutdownCtx)
			httpServer.Shutdown(shutdownCtx)
			node.Close()

			logger.Info("shutdown complete")
			os.Exit(0)
		}
	}
}

func applyRoutes(cfg *config.Config, router *proxy.Router, checker *health.Checker) {
	router.SetRoutes(cfg.Routes)

	var backends []health.Backend
	for _, r := range cfg.Routes {
		if r.HealthCheck != nil {
			backends = append(backends, health.Backend{
				Domain:   r.Domain,
				Addr:     r.Backend,
				Path:     r.HealthCheck.Path,
				Interval: r.HealthCheck.Interval,
			})
		}
	}
	checker.Start(backends)
}
