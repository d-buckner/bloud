// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"github.com/go-chi/chi/v5"
)

// appLoadingHTML is the per-app waiting page. It is the app-domain counterpart
// of loading.html: that one covers Bloud's own catch-all during first boot,
// this one covers an individual app whose container is down while the route
// for its domain is still live.
//
//go:embed app_loading.html
var appLoadingHTML []byte

// appLoadingTemplate is parsed once at init. The template is a build-time
// artifact like the page it renders, so a malformed template is a startup
// failure rather than a per-request one.
var appLoadingTemplate = template.Must(template.New("app_loading").Parse(string(appLoadingHTML)))

// loadingHeader marks a response as Bloud's waiting page rather than the
// app's own content. The page reads it back on its own URL to decide whether
// the app is serving yet, which is why it has to be on the response and not
// only in the body.
const loadingHeader = "X-Bloud-Loading"

// catalogIDPattern is the shape a catalog ID has: lowercase alphanumerics and
// hyphens, starting with an alphanumeric. The waiting-page route puts the
// name it is given into a filesystem path and into a rendered page, so the
// name is validated against what an ID can actually be rather than screened
// for characters that look dangerous. An allowlist of the real shape closes
// traversal, encoded traversal, and anything that is not an app, in one rule.
var catalogIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// appLoadingData is what the template renders.
type appLoadingData struct {
	AppID       string
	DisplayName string
	// IconDataURI is the app's icon inlined as `data:image/png;base64,...`,
	// empty when the app ships none.
	IconDataURI string
	// Initial is the letter avatar shown when there is no icon, the same
	// fallback the dashboard grid uses.
	Initial string
}

// renderAppLoading fills the waiting-page template for one app.
func renderAppLoading(data appLoadingData) (string, error) {
	var buf bytes.Buffer
	if err := appLoadingTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render app loading page: %w", err)
	}
	return buf.String(), nil
}

// appLoadingDataFor builds the render input for one app: the display name from
// the catalog when it is known, the icon from the catalog directory when it
// exists.
//
// The icon is inlined rather than referenced by URL because this page is served
// on the app's own domain. The icon's usual route lives under Bloud's API host,
// and on `<app>.<domain>` the app's own router outranks it, so an <img> tag
// would point at the very container that is not answering.
func appLoadingDataFor(cat catalog.CacheInterface, appsDir, name string) appLoadingData {
	data := appLoadingData{AppID: name, DisplayName: name}
	if cat != nil {
		if app, err := cat.Get(name); err == nil && app != nil && app.DisplayName != "" {
			data.DisplayName = app.DisplayName
		}
	}
	if uri := appIconDataURI(appsDir, name); uri != "" {
		data.IconDataURI = uri
	} else {
		data.Initial = letterInitial(data.DisplayName)
	}
	return data
}

// appIconDataURI reads the app's icon and returns it as a data URI, or "" when
// there is no readable icon. The size cap is there because the result is
// embedded in every copy of the page: an icon is a small PNG by convention,
// and a 5 MB one would be a bug worth refusing rather than shipping.
func appIconDataURI(appsDir, name string) string {
	if appsDir == "" || name == "" {
		return ""
	}
	iconPath := filepath.Join(appsDir, name, "icon.png")
	const maxIconBytes = 512 * 1024
	info, err := os.Stat(iconPath)
	if err != nil || info.Size() == 0 || info.Size() > maxIconBytes {
		return ""
	}
	raw, err := os.ReadFile(iconPath)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}

// letterInitial returns the first letter of the display name, uppercased, for
// the no-icon avatar.
func letterInitial(displayName string) string {
	for _, r := range displayName {
		if r == ' ' || r == '-' || r == '_' {
			continue
		}
		return strings.ToUpper(string(r))
	}
	return "?"
}

// AppLoadingHandler serves the waiting page for one app.
//
// It answers 503 rather than 200. Traefik's error middleware replays this
// body and these headers in place of the app's own Bad Gateway, but keeps the
// original upstream status, so a visitor on the app's domain sees 502 with
// this page rather than Traefik's. Either way the status is an error status,
// which is the part that matters: a 200 would let a cache store the waiting
// page as the app's real response, and would tell a client that followed the
// request that it got the app when it got neither.
//
// Retry-After is on the response for the same reason. Anything polling the
// domain while the app is down is told how long to wait, by the same header
// whether it arrives through the proxy or straight at the agent.
func (m *appsModule) AppLoadingHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		// Unescape first: the router hands over the escaped form, so `%2e%2e%2f`
		// would otherwise sail past a check for `/` and only be decoded later,
		// if at all, by whatever reads the path.
		if unescaped, err := url.PathUnescape(name); err == nil {
			name = unescaped
		}
		if !catalogIDPattern.MatchString(name) {
			http.NotFound(w, r)
			return
		}

		body, err := renderAppLoading(appLoadingDataFor(m.catalog, m.appsDir, name))
		if err != nil {
			m.logger.Error("failed to render the app loading page", "app", name, "error", err)
			http.Error(w, "failed to render loading page", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "2")
		w.Header().Set(loadingHeader, name)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(body))
	}
}
