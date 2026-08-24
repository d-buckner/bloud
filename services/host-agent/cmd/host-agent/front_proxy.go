// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// The front proxy is a thin, standalone reverse proxy that listens on port 80
// and forwards to the Traefik entrypoint (port 8080 by default). It exists as
// a separate process (run as a root systemd service next to the rootless
// host-agent stack) so that:
//
//  1. Traefik (and everything else Bloud runs) stays rootless — no container
//     needs to bind a privileged port.
//  2. While the host-agent and Traefik are still starting up, users get a
//     friendly "starting up" page on port 80 instead of a connection refused
//     or a raw Traefik 503.
//
// Readiness is gated on the host-agent API (GET /api/health), which only
// opens after the system apps have converged, so the fallback page is shown
// for the entire bootstrap window.

const (
	frontPortEnv        = "BLOUD_FRONT_PORT"
	traefikPortEnv      = "BLOUD_TRAEFIK_PORT"
	hostAgentPortEnv    = "BLOUD_PORT"
	healthCheckTimeout  = 500 * time.Millisecond
	readyCacheTTL       = 2 * time.Second
	unreadyCacheTTL     = time.Second
	fallbackReloadSecs  = 5
)

// frontProxy forwards requests to Traefik when the host-agent reports ready,
// and serves a fallback startup page otherwise.
type frontProxy struct {
	agentHealthURL string // e.g. http://127.0.0.1:3000/api/health
	traefikAddr    string // e.g. 127.0.0.1:8080
	client         *http.Client
	proxy          *httputil.ReverseProxy
	logger         *slog.Logger

	cacheMu     sync.Mutex
	lastChecked time.Time
	lastReady   bool
}

// newFrontProxy builds the proxy from environment configuration.
func newFrontProxy(logger *slog.Logger) (*frontProxy, error) {
	agentPort := envInt(hostAgentPortEnv, 3000)
	traefikPort := envInt(traefikPortEnv, 8080)

	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", traefikPort))
	if err != nil {
		return nil, fmt.Errorf("parsing traefik target: %w", err)
	}

	f := &frontProxy{
		agentHealthURL: fmt.Sprintf("http://127.0.0.1:%d/api/health", agentPort),
		traefikAddr:    target.Host,
		client:         &http.Client{Timeout: healthCheckTimeout},
		logger:         logger,
	}
	f.proxy = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = f.traefikAddr
			// req.Host is intentionally left untouched: Traefik routes on
			// the original Host header (HostRegexp rules).
		},
		// Stream responses (SSE, large downloads) without buffering.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// Backend went away mid-flight (e.g. host-agent restarted):
			// fall back to the startup page.
			f.logger.Warn("proxy error, serving fallback", "error", err)
			f.serveFallback(w, false)
		},
	}
	return f, nil
}

// envInt reads a positive integer environment variable or returns the default.
func envInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultValue
}

// agentReady reports whether the host-agent API answers /api/health. Results
// are cached briefly so the hot path (stack fully up) does one cheap local
// check per TTL window instead of per request: 2s when ready, 1s while the
// stack is still coming up (so the fallback flips to the dashboard quickly).
func (f *frontProxy) agentReady(ctx context.Context) bool {
	f.cacheMu.Lock()
	window := unreadyCacheTTL
	if f.lastReady {
		window = readyCacheTTL
	}
	if time.Since(f.lastChecked) < window {
		ready := f.lastReady
		f.cacheMu.Unlock()
		return ready
	}
	f.cacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.agentHealthURL, nil)
	if err != nil {
		return false
	}
	resp, err := f.client.Do(req)
	ready := err == nil && resp.StatusCode == http.StatusOK
	if resp != nil {
		resp.Body.Close()
	}

	f.cacheMu.Lock()
	f.lastChecked = time.Now()
	f.lastReady = ready
	f.cacheMu.Unlock()
	return ready
}

// traefikUp is a best-effort probe used only to pick the fallback page copy.
func (f *frontProxy) traefikUp(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+f.traefikAddr+"/ping", nil)
	if err != nil {
		return false
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ServeHTTP implements http.Handler: proxy when ready, fallback page when not.
func (f *frontProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.agentReady(r.Context()) {
		f.proxy.ServeHTTP(w, r)
		return
	}
	f.serveFallback(w, f.traefikUp(r.Context()))
}

func (f *frontProxy) serveFallback(w http.ResponseWriter, traefikUp bool) {
	detail := "starting up… checking services"
	if traefikUp {
		detail = "provisioning system services…"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(fallbackPageHTML(detail))
}

// fallbackPageHTML renders the standalone startup page. It auto-reloads so
// the browser lands on the real dashboard as soon as the stack is ready.
func fallbackPageHTML(detail string) []byte {
	return []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="` + strconv.Itoa(fallbackReloadSecs) + `">
<title>Bloud — starting up</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; margin: 0; }
  body {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: ui-serif, Georgia, serif;
    background: #f7f4f0;
    color: #1c1917;
  }
  @media (prefers-color-scheme: dark) {
    body { background: #16130f; color: #f5f0ea; }
  }
  main { text-align: center; padding: 2rem; }
  h1 { font-size: 2.5rem; font-weight: 500; letter-spacing: 0.02em; }
  .status { margin-top: 1rem; font-size: 1.05rem; opacity: 0.8; }
  .hint { margin-top: 0.5rem; font-size: 0.85rem; opacity: 0.5; font-family: ui-sans-serif, system-ui, sans-serif; }
  .pulse {
    display: inline-block;
    width: 0.6em; height: 0.6em;
    border-radius: 50%;
    background: #b45309;
    margin-right: 0.5em;
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.25; } }
</style>
</head>
<body>
<main>
  <h1>Bloud</h1>
  <p class="status"><span class="pulse"></span>` + detail + `</p>
  <p class="hint">This page reloads automatically once your server is ready.</p>
</main>
</body>
</html>
`)}

// runFrontProxy runs the port-80 front proxy until interrupted.
func runFrontProxy() int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	f, err := newFrontProxy(logger)
	if err != nil {
		logger.Error("failed to create front proxy", "error", err)
		return 1
	}

	port := envInt(frontPortEnv, 80)
	addr := fmt.Sprintf(":%d", port)
	logger.Info("front proxy starting", "listen", addr, "target", f.traefikAddr, "health", f.agentHealthURL)

	server := &http.Server{
		Addr:      addr,
		Handler:   f,
		// No request timeout: long-lived streams (SSE) are proxied through.
		IdleTimeout: 120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		logger.Error("front proxy failed", "error", err)
		return 1
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		logger.Info("front proxy stopped")
		return 0
	}
}
