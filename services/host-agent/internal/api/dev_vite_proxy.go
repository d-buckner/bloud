// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

// DevViteURLEnv points the dashboard at a running `vite dev` server instead of
// the static bundle. It is a development-only switch: production never sets it
// and keeps serving the static build (invariant 11).
//
// The proxy exists so the browser stays on the origin it always uses. The
// dashboard is an OIDC client of Authentik and its redirect URIs are registered
// per configured host up front (see LoginHandler), so pointing a browser
// straight at vite's own origin would break login: the callback would arrive on
// an origin the client was never issued for. Proxying here means the page, the
// API, the session cookie, and the OAuth round trip all keep the same origin
// they have in the real deployment, while the modules themselves come from a
// dev server that hot-reloads on save.
const DevViteURLEnv = "BLOUD_DEV_VITE_URL"

// viteDevProxy builds the handler that forwards frontend requests to the vite
// dev server. The reverse proxy upgrades WebSocket connections on its own, so
// the HMR channel rides the same path as the module requests and needs no
// second listener exposed to the browser.
func viteDevProxy(rawURL string, logger *slog.Logger) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", DevViteURLEnv, err)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("%s %q has no host", DevViteURLEnv, rawURL)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, fmt.Errorf("%s must be an http(s) URL, got %q", DevViteURLEnv, target.Scheme)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Present the dev server with its own host. `NewSingleHostReverseProxy`
	// rewrites the URL but forwards the incoming Host header verbatim, and
	// vite answers that header with its `allowedHosts` guard: a request for
	// an operator's own domain or a LAN address gets a 403 "Blocked request"
	// while `localhost` works, which reads as a routing failure rather than
	// a host check. Rewriting to the target host keeps vite out of the
	// picture entirely; the browser still never leaves the origin it was
	// issued OAuth redirects for, because that is set by the URL bar, not
	// by this header.
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = target.Host
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, proxyErr error) {
		// A dead dev server is a common and transient state: vite is still
		// restarting, or was stopped while host-agent kept running. Say which,
		// rather than letting the browser show an opaque failure.
		logger.Warn("vite dev proxy failed", "path", r.URL.Path, "target", target.String(), "error", proxyErr)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, fmt.Sprintf("vite dev server unreachable at %s: %v", target.String(), proxyErr), http.StatusBadGateway)
	}
	return proxy, nil
}

// viteDevProxyFromEnv resolves DevViteURLEnv into a proxy handler. The bool is
// false when the switch is unset, which is the product path: callers fall
// through to the static bundle. A set-but-unusable value is reported as an
// error rather than silently disabling the switch, so a typo does not turn into
// "my changes are not showing up".
func viteDevProxyFromEnv(logger *slog.Logger) (http.Handler, bool, error) {
	raw := os.Getenv(DevViteURLEnv)
	if raw == "" {
		return nil, false, nil
	}
	handler, err := viteDevProxy(raw, logger)
	if err != nil {
		return nil, true, err
	}
	return handler, true, nil
}

// viteProxyErrorBody is what the frontend handler writes when the configured
// proxy could not be built. It is deliberately explicit about the env var: the
// failure mode it guards against is a developer staring at a stale bundle.
func viteProxyErrorBody(err error) string {
	return fmt.Sprintf("%s is set but unusable: %v", DevViteURLEnv, errors.Unwrap(err))
}
