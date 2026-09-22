// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBootstrapGate(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		_, _ = w.Write([]byte("real ui:" + r.URL.Path))
	})
	ready := make(chan struct{})
	gated := bootstrapGate(ready, next)

	serve := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		nextCalled = false
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	t.Run("UI path serves the loading page before convergence", func(t *testing.T) {
		rec := serve("/")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("Content-Type = %q, want text/html", ct)
		}
		if !strings.Contains(rec.Body.String(), "Getting your home cloud ready") {
			t.Fatalf("body is not the loading page: %q", rec.Body.String())
		}
		if nextCalled {
			t.Fatal("the gated handler ran before convergence")
		}
	})

	t.Run("loading page polls the gate and the sign-in status", func(t *testing.T) {
		body := serve("/").Body.String()
		// Stage one is the gate, stage two is the live identity-provider probe.
		// A single status code cannot tell "still booting" from "up and broken".
		if !strings.Contains(body, "/api/health") {
			t.Error("the page does not poll the gate at /api/health")
		}
		if !strings.Contains(body, "/api/setup/status") {
			t.Error("the page does not confirm sign-in at /api/setup/status")
		}
	})

	t.Run("brand assets pass through before convergence", func(t *testing.T) {
		// The loading page loads the real Bloud fonts and favicon. Gating those
		// would hand the browser HTML in place of a font file, and the page
		// would render in a fallback face.
		for _, path := range []string{"/fonts/inter-latin.woff2", "/favicon.svg"} {
			rec := serve(path)
			if !nextCalled {
				t.Errorf("%s did not reach the frontend handler", path)
			}
			if strings.Contains(rec.Body.String(), "Getting your home cloud ready") {
				t.Errorf("%s got the loading page instead of the asset", path)
			}
		}
	})

	t.Run("health probe answers starting, not the loading page", func(t *testing.T) {
		// A monitor pointed at /health must not read 200 with HTML while the
		// system is still coming up.
		rec := serve("/health")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}
		if !strings.Contains(rec.Body.String(), `"error":"starting"`) {
			t.Fatalf("body = %q, want the starting envelope", rec.Body.String())
		}
		if nextCalled {
			t.Fatal("the gated handler ran before convergence")
		}
	})

	t.Run("API path is unavailable before convergence", func(t *testing.T) {
		rec := serve("/api/apps")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if nextCalled {
			t.Fatal("the gated handler ran before convergence")
		}
	})

	t.Run("delegates once the orchestrator is ready", func(t *testing.T) {
		close(ready)
		if rec := serve("/"); rec.Body.String() != "real ui:/" {
			t.Fatalf("body = %q, want the wrapped handler's output", rec.Body.String())
		}
		if !nextCalled {
			t.Fatal("the gated handler did not run after convergence")
		}
	})
}
