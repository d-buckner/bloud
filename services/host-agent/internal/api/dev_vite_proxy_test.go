// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newFrontendOnlyRouter is a bare router: the frontend helper registers a
// catch-all on it, which is the shape production uses minus every API route.
func newFrontendOnlyRouter() chi.Router { return chi.NewRouter() }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (*discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestViteDevProxyForwardsRequests(t *testing.T) {
	var gotPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("from vite:" + r.URL.Path))
	}))
	defer upstream.Close()

	proxy, err := viteDevProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatalf("viteDevProxy: %v", err)
	}

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard?x=1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.HasPrefix(body, "from vite:/dashboard") {
		t.Fatalf("body = %q, want the upstream response", body)
	}
	if len(gotPaths) != 1 || gotPaths[0] != "/dashboard" {
		t.Fatalf("upstream saw %v, want [/dashboard]", gotPaths)
	}
}

func TestViteDevProxyRejectsBadTargets(t *testing.T) {
	cases := []string{
		"://nope",
		"not a url",
		"ftp://localhost:5173",
		"/relative/only",
		"",
	}
	for _, raw := range cases {
		if _, err := viteDevProxy(raw, discardLogger()); err == nil {
			t.Errorf("viteDevProxy(%q) = nil error, want a rejection", raw)
		}
	}
}

func TestViteDevProxyReportsAnUnreachableUpstream(t *testing.T) {
	// A dev server that is down is the common case, not a mystery: the
	// response has to name the target rather than surface a bare 502.
	proxy, err := viteDevProxy("http://127.0.0.1:1", discardLogger())
	if err != nil {
		t.Fatalf("viteDevProxy: %v", err)
	}
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "vite dev server unreachable") {
		t.Fatalf("body = %q, want it to name the failure", body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store on an error response", cc)
	}
}

func TestViteDevProxyFromEnv(t *testing.T) {
	t.Run("unset means the static bundle path", func(t *testing.T) {
		t.Setenv(DevViteURLEnv, "")
		handler, configured, err := viteDevProxyFromEnv(discardLogger())
		if configured {
			t.Fatal("configured = true with the env var unset")
		}
		if handler != nil || err != nil {
			t.Fatalf("handler = %v, err = %v, want nil/nil when unset", handler, err)
		}
	})

	t.Run("set and valid returns a working proxy", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("vite upstream"))
		}))
		defer upstream.Close()

		t.Setenv(DevViteURLEnv, upstream.URL)
		handler, configured, err := viteDevProxyFromEnv(discardLogger())
		if err != nil || !configured || handler == nil {
			t.Fatalf("configured=%v handler=%v err=%v, want true/non-nil/nil", configured, handler, err)
		}

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if body := rec.Body.String(); body != "vite upstream" {
			t.Fatalf("body = %q, want the upstream body", body)
		}
	})

	t.Run("set but broken is reported, not silently ignored", func(t *testing.T) {
		// The failure this guards against is a developer staring at a stale
		// bundle because of a typo in the env var.
		t.Setenv(DevViteURLEnv, "://bogus")
		handler, configured, err := viteDevProxyFromEnv(discardLogger())
		if !configured {
			t.Fatal("configured = false with the env var set")
		}
		if err == nil {
			t.Fatal("err = nil with an unusable target")
		}
		if handler != nil {
			t.Fatal("handler should be nil when the target is unusable")
		}
	})
}

// TestSetupFrontendHelperUsesViteProxy pins the switch end to end at the
// handler level: with the env var set the frontend handler must go to vite and
// must not read the static bundle.
func TestSetupFrontendHelperUsesViteProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PROXIED" + r.URL.Path))
	}))
	defer upstream.Close()

	t.Setenv(DevViteURLEnv, upstream.URL)

	// The helper resolves the static bundle relative to the working
	// directory; point it somewhere with no build so a fall-through would be
	// visible as the fallback dashboard rather than the proxy body.
	t.Chdir(t.TempDir())

	router := newFrontendOnlyRouter()
	setupFrontendHelper(router, discardLogger())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/page", nil))

	if body := rec.Body.String(); !strings.HasPrefix(body, "PROXIED/some/page") {
		t.Fatalf("body = %q, want the vite proxy response", body)
	}
}

// TestSetupFrontendHelperServesStaticWhenUnset is the product path: no env
// var, no proxy, and the missing-build fallback instead.
func TestSetupFrontendHelperServesStaticWhenUnset(t *testing.T) {
	t.Setenv(DevViteURLEnv, "")
	t.Chdir(t.TempDir())

	router := newFrontendOnlyRouter()
	setupFrontendHelper(router, discardLogger())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "PROXIED") || strings.Contains(body, "BLOUD_DEV_VITE_URL") {
		t.Fatalf("body = %q, want the fallback dashboard, not a proxy response", body)
	}
}

// TestSetupFrontendHelperReportsUnusableTarget keeps a typo from turning into a
// silently stale dashboard.
func TestSetupFrontendHelperReportsUnusableTarget(t *testing.T) {
	t.Setenv(DevViteURLEnv, "://bogus")
	t.Chdir(t.TempDir())

	router := newFrontendOnlyRouter()
	setupFrontendHelper(router, discardLogger())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, DevViteURLEnv) {
		t.Fatalf("body = %q, want it to name %s", body, DevViteURLEnv)
	}
}

// TestDevViteURLEnvNameIsStable guards the contract the CLI writes against the
// one the host-agent reads. They are separate modules, so the string is the
// only thing tying them together.
func TestDevViteURLEnvNameIsStable(t *testing.T) {
	if DevViteURLEnv != "BLOUD_DEV_VITE_URL" {
		t.Fatalf("DevViteURLEnv = %q, want BLOUD_DEV_VITE_URL (the CLI sets this literal)", DevViteURLEnv)
	}
	if _, ok := os.LookupEnv("BLOUD_DEV_VITE_URL"); ok {
		t.Log("note: BLOUD_DEV_VITE_URL is set in the ambient environment")
	}
}
