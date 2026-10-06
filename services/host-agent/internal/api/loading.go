// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	_ "embed"
	"net/http"
	"strings"
)

// loadingHTML is the static page served while the system apps converge.
//
//go:embed loading.html
var loadingHTML []byte

// gatedAssetPrefixes are the paths that pass through the gate while the system
// apps converge: the frontend's static brand assets, and the per-app waiting
// page.
//
// The fonts and the favicon are plain files on disk with no dependency on the
// orchestrator, so they pass through. Without the exception the loading page
// would get loading page HTML back for its own font requests, and render in a
// fallback face instead of the Bloud type.
//
// `/bloud-loading/` passes because it is itself a waiting page. Traefik's
// error middleware fetches it while an app is down, and during bootstrap an
// app is down; answering that with the first-boot page would replace a message
// about the app the visitor asked for with one about the whole system.
var gatedAssetPrefixes = []string{"/fonts/", "/favicon.", "/bloud-loading/"}

// passesThroughGate reports whether a gated request is a static brand asset the
// frontend handler can serve straight from disk.
func passesThroughGate(path string) bool {
	for _, prefix := range gatedAssetPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// answersStarting reports the paths that must answer 503 {"error":"starting"}
// while the system converges: the API surface, and the root health probe.
//
// The health probe is in that set on purpose. Left out, the gate would answer it
// with the loading page HTML at status 200 during bootstrap, and a monitor
// pointed at /health would read green while nothing is up yet. Once the gate
// opens the real handler takes over and answers on its own merits, which is a
// different 503: {"status":"unhealthy"}.
func answersStarting(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/") || path == "/health"
}

// bootstrapGate serves a static loading page until the orchestrator reports
// ready. host-agent opens its listener at process start (so Traefik can proxy
// to it), but the API it fronts is not usable until the system apps are
// running. While they converge, the API surface and the health probe answer 503
// and every other path gets loadingHTML, which polls in the background and
// reloads only once the dashboard is really there. Without the gate a browser
// hitting Traefik during bootstrap would see Traefik's own 502. Static brand
// assets are the one exception, so the waiting page looks like Bloud (see
// gatedAssetPrefixes).
func bootstrapGate(ready <-chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ready:
			next.ServeHTTP(w, r)
			return
		default:
		}

		if passesThroughGate(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		if answersStarting(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"starting"}`))
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(loadingHTML)
	})
}
