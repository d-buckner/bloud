// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

func loadingModule(t *testing.T, appsDir string, apps ...*catalog.App) *appsModule {
	t.Helper()
	cache := NewFakeCatalogCache()
	for _, app := range apps {
		addAppToCache(cache, app)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mod := NewAppsModule(cache, NewFakeAppStore(), newFakeOrchestrator(), logger)
	mod.SetAppsDir(appsDir)
	return mod
}

// serveLoading routes the handler through a chi mux so the URL param is
// populated the way it is in the real router.
func serveLoading(t *testing.T, mod *appsModule, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/bloud-loading/{name}", mod.AppLoadingHandler())
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func writeAppIcon(t *testing.T, appsDir, app string) []byte {
	t.Helper()
	dir := filepath.Join(appsDir, app)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// A one-pixel PNG. The handler does not decode it; it only needs bytes
	// that exist and are small.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "icon.png"), png, 0o644))
	return png
}

// The page has to name the app the visitor asked for, not Bloud in general.
func TestAppLoading_ShowsTheAppsDisplayNameAndIcon(t *testing.T) {
	appsDir := t.TempDir()
	writeAppIcon(t, appsDir, "jellyfin")
	mod := loadingModule(t, appsDir, &catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin"})

	rec := serveLoading(t, mod, "/bloud-loading/jellyfin")

	body := rec.Body.String()
	assert.Contains(t, body, "Jellyfin is re-loading")
	assert.Contains(t, body, "Waiting for Jellyfin to come back")
	// The JS escaper turns `/` into `\/` inside a JS string literal, so the
	// assertion is on the prefix rather than the exact URI.
	assert.Contains(t, body, `var APP_ICON = "data:image`)
	assert.NotContains(t, body, `var APP_ICON = "";`)
	// With an icon there is no letter to show. The avatar markup is still in
	// the page as the no-JS fallback, but empty, so a browser that runs the
	// script swaps it for the icon and one that does not shows a plain block
	// rather than a wrong letter beside a right icon.
	assert.NotContains(t, body, ">J</div>")
}

// An app with no icon falls back to the letter avatar, the same fallback the
// dashboard grid uses, so the page never shows a broken image.
func TestAppLoading_FallsBackToLetterAvatar(t *testing.T) {
	mod := loadingModule(t, t.TempDir(), &catalog.App{CatalogID: "seerr", DisplayName: "Seerr"})

	body := serveLoading(t, mod, "/bloud-loading/seerr").Body.String()

	assert.Contains(t, body, `id="app-avatar"`)
	assert.Contains(t, body, `>S</div>`)
	assert.Contains(t, body, `var APP_ICON = "";`)
}

// The catalog can be missing the app (a rename mid-flight, a catalog refresh
// that has not landed). The page still has to render, keyed on the id.
func TestAppLoading_UnknownAppStillRenders(t *testing.T) {
	mod := loadingModule(t, t.TempDir())

	body := serveLoading(t, mod, "/bloud-loading/mystery").Body.String()

	assert.Contains(t, body, "mystery is re-loading")
	assert.Contains(t, body, `>M</div>`)
}

// The status is 503, not 200. Traefik's error middleware replays this status
// in place of the app's 502, so this is what every client sees while the app
// reconciles. A browser renders the body whatever the status; an API client
// that hits the domain mid-reconcile needs a retryable code, not a 200 whose
// body is HTML.
func TestAppLoading_RespondsRetryable503WithTheMarkerHeader(t *testing.T) {
	mod := loadingModule(t, t.TempDir(), &catalog.App{CatalogID: "radarr", DisplayName: "Radarr"})

	rec := serveLoading(t, mod, "/bloud-loading/radarr")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "radarr", rec.Header().Get("X-Bloud-Loading"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "2", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
}

// The marker header is what the page polls for. Without it the page could
// never tell its own waiting screen from the app's real content, and would
// either reload forever or never reload at all.
func TestAppLoading_MarkerHeaderIsOnEverySuccessfulRender(t *testing.T) {
	mod := loadingModule(t, t.TempDir())
	rec := serveLoading(t, mod, "/bloud-loading/anything")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotEmpty(t, rec.Header().Get("X-Bloud-Loading"))
}

// A display name is user-editable through the rename route, so it reaches this
// template as untrusted text. It must not be able to close the title tag and
// run as markup.
func TestAppLoading_EscapesADisplayNameThatTriesToBeMarkup(t *testing.T) {
	mod := loadingModule(t, t.TempDir(), &catalog.App{
		CatalogID:   "evil",
		DisplayName: `</title><script>alert(1)</script>`,
	})

	body := serveLoading(t, mod, "/bloud-loading/evil").Body.String()

	assert.NotContains(t, body, "<script>alert(1)</script>")
	assert.Contains(t, body, "&lt;/title&gt;")
	assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

// The name goes into a filesystem path. An allowlist of the shape a catalog ID
// actually has is what closes that, including the encoded forms: `%2e%2e%2f`
// decodes to `../` only after the router hands the value over, so the check
// runs on the unescaped string.
func TestAppLoading_RejectsNamesThatAreNotCatalogIDs(t *testing.T) {
	mod := loadingModule(t, t.TempDir())

	for _, path := range []string{
		"/bloud-loading/..%2F..%2Fetc",
		"/bloud-loading/%2e%2e%2fpasswd",
		"/bloud-loading/%2E%2E/passwd",
		"/bloud-loading/Jellyfin",
		"/bloud-loading/jelly_fin",
		"/bloud-loading/jellyfin%20etc",
		"/bloud-loading/-leading",
		"/bloud-loading/%3Cscript%3E",
	} {
		rec := serveLoading(t, mod, path)
		assert.Equal(t, http.StatusNotFound, rec.Code, "path %q must not render a page", path)
	}
}

// A hyphenated catalog ID is the common case, not an edge case.
func TestAppLoading_AcceptsAHyphenatedCatalogID(t *testing.T) {
	mod := loadingModule(t, t.TempDir(), &catalog.App{CatalogID: "affine-mcp", DisplayName: "AFFiNE MCP"})

	rec := serveLoading(t, mod, "/bloud-loading/affine-mcp")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "affine-mcp", rec.Header().Get("X-Bloud-Loading"))
}

func TestLetterInitial(t *testing.T) {
	cases := map[string]string{
		"Jellyfin":  "J",
		"AFFiNE":    "A",
		"Paperless": "P",
		"  spaced":  "S",
		"-dash":     "D",
		"":          "?",
		"   ":       "?",
	}
	for in, want := range cases {
		assert.Equal(t, want, letterInitial(in), "input %q", in)
	}
}

// The icon is inlined because the page is served on the app's own domain,
// where the icon's usual route would hit the container that is not answering.
// A missing, empty, or absurdly large icon must degrade to the avatar rather
// than bloat or break the page.
func TestAppIconDataURI(t *testing.T) {
	appsDir := t.TempDir()

	assert.Empty(t, appIconDataURI("", "jellyfin"), "no apps dir means no icon")
	assert.Empty(t, appIconDataURI(appsDir, ""), "no app name means no icon")
	assert.Empty(t, appIconDataURI(appsDir, "absent"), "no file means no icon")

	writeAppIcon(t, appsDir, "jellyfin")
	uri := appIconDataURI(appsDir, "jellyfin")
	assert.True(t, strings.HasPrefix(uri, "data:image/png;base64,"), uri)

	// An empty file is not an icon.
	require.NoError(t, os.WriteFile(filepath.Join(appsDir, "jellyfin", "icon.png"), nil, 0o644))
	assert.Empty(t, appIconDataURI(appsDir, "jellyfin"))

	// A file past the cap is refused rather than embedded.
	huge := make([]byte, 600*1024)
	require.NoError(t, os.WriteFile(filepath.Join(appsDir, "jellyfin", "icon.png"), huge, 0o644))
	assert.Empty(t, appIconDataURI(appsDir, "jellyfin"))
}

// The template is parsed at init. If it ever stops parsing, that has to be a
// loud failure rather than a page that silently renders nothing.
func TestAppLoadingTemplateParses(t *testing.T) {
	require.NotNil(t, appLoadingTemplate)
	out, err := renderAppLoading(appLoadingData{
		AppID: "jellyfin", DisplayName: "Jellyfin", Initial: "J",
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Jellyfin is re-loading")
	assert.Contains(t, out, "X-Bloud-Loading")
}
