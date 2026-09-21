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
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		_, _ = w.Write([]byte("real ui"))
	})
	ready := make(chan struct{})
	gated := bootstrapGate(ready, next)

	serve := func(path string) *httptest.ResponseRecorder {
		t.Helper()
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
		if !strings.Contains(rec.Body.String(), "Bloud is starting") {
			t.Fatalf("body is not the loading page: %q", rec.Body.String())
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
		if rec := serve("/"); rec.Body.String() != "real ui" {
			t.Fatalf("body = %q, want the wrapped handler's output", rec.Body.String())
		}
		if !nextCalled {
			t.Fatal("the gated handler did not run after convergence")
		}
	})
}
