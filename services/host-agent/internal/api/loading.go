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

// bootstrapGate serves a static loading page until the orchestrator reports
// ready. host-agent opens its listener at process start (so Traefik can proxy
// to it), but the API it fronts is not usable until the system apps are
// running. While they converge, /api answers 503 and every other path gets
// loadingHTML, which reloads itself and lands on the real UI once convergence
// completes. Without the gate a browser hitting Traefik during bootstrap would
// see Traefik's own 502.
func bootstrapGate(ready <-chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ready:
			next.ServeHTTP(w, r)
			return
		default:
		}

		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
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
