// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gateProbe drives a gated handler and records whether the wrapped handler ran.
type gateProbe struct {
	nextCalled bool
	gated      http.Handler
}

func newGateProbe() (*gateProbe, chan struct{}) {
	ready := make(chan struct{})
	p := &gateProbe{}
	p.gated = bootstrapGate(ready, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.nextCalled = true
		_, _ = w.Write([]byte("real ui:" + r.URL.Path))
	}))
	return p, ready
}

// serve runs one request through the gate with the delegation flag reset.
func (p *gateProbe) serve(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	p.nextCalled = false
	rec := httptest.NewRecorder()
	p.gated.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// assertNotDelegated asserts the wrapped handler never ran, which is what makes
// a pre-convergence response the gate's own answer rather than the app's.
func (p *gateProbe) assertNotDelegated(t *testing.T) {
	t.Helper()
	if p.nextCalled {
		t.Fatal("the gated handler ran before convergence")
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d", rec.Code, code)
	}
}

func assertContentType(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, want) {
		t.Fatalf("Content-Type = %q, want %s", ct, want)
	}
}

func assertBodyContains(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body does not contain %q: %q", want, rec.Body.String())
	}
}

func assertBodyExcludes(t *testing.T, rec *httptest.ResponseRecorder, unwanted string) {
	t.Helper()
	if strings.Contains(rec.Body.String(), unwanted) {
		t.Fatalf("body should not contain %q: %q", unwanted, rec.Body.String())
	}
}

func TestBootstrapGate(t *testing.T) {
	probe, ready := newGateProbe()

	t.Run("UI path serves the loading page before convergence", func(t *testing.T) {
		rec := probe.serve(t, "/")
		assertStatus(t, rec, http.StatusOK)
		assertContentType(t, rec, "text/html")
		assertBodyContains(t, rec, "Getting your home cloud ready")
		probe.assertNotDelegated(t)
	})

	t.Run("loading page polls the gate and the sign-in status", func(t *testing.T) {
		rec := probe.serve(t, "/")
		// Stage one is the gate, stage two is the live identity-provider probe.
		// A single status code cannot tell "still booting" from "up and broken".
		assertBodyContains(t, rec, "/api/health")
		assertBodyContains(t, rec, "/api/setup/status")
	})

	t.Run("brand assets pass through before convergence", func(t *testing.T) {
		// The loading page loads the real Bloud fonts and favicon. Gating those
		// would hand the browser HTML in place of a font file, and the page
		// would render in a fallback face.
		for _, path := range []string{"/fonts/inter-latin.woff2", "/favicon.svg"} {
			rec := probe.serve(t, path)
			if !probe.nextCalled {
				t.Errorf("%s did not reach the frontend handler", path)
			}
			assertBodyExcludes(t, rec, "Getting your home cloud ready")
		}
	})

	t.Run("health probe answers starting, not the loading page", func(t *testing.T) {
		// A monitor pointed at /health must not read 200 with HTML while the
		// system is still coming up.
		rec := probe.serve(t, "/health")
		assertStatus(t, rec, http.StatusServiceUnavailable)
		assertContentType(t, rec, "application/json")
		assertBodyContains(t, rec, `"error":"starting"`)
		probe.assertNotDelegated(t)
	})

	t.Run("API path is unavailable before convergence", func(t *testing.T) {
		rec := probe.serve(t, "/api/apps")
		assertStatus(t, rec, http.StatusServiceUnavailable)
		probe.assertNotDelegated(t)
	})

	t.Run("delegates once the orchestrator is ready", func(t *testing.T) {
		close(ready)
		rec := probe.serve(t, "/")
		if rec.Body.String() != "real ui:/" {
			t.Fatalf("body = %q, want the wrapped handler's output", rec.Body.String())
		}
		if !probe.nextCalled {
			t.Fatal("the gated handler did not run after convergence")
		}
	})
}
