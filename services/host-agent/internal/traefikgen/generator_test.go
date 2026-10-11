// SPDX-License-Identifier: AGPL-3.0-only

package traefikgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

func TestGenerator_Generate_EmptyApps(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	g := NewGenerator(configPath)
	err := g.Generate(nil)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !strings.Contains(string(content), "# No routable apps installed") {
		t.Errorf("Expected 'No routable apps' message, got:\n%s", content)
	}
}

func TestGenerator_Generate_SystemAppsFiltered(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{CatalogID: "postgres", Port: 5432, IsSystem: true},
		{CatalogID: "traefik", Port: 8080, IsSystem: true},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// System apps should be filtered out
	if !strings.Contains(string(content), "# No routable apps installed") {
		t.Errorf("System apps should be filtered, got:\n%s", content)
	}
}

func TestGenerator_Generate_BasicApp(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Check router uses HostRegexp rule
	if !strings.Contains(contentStr, "miniflux:") {
		t.Error("Expected miniflux router")
	}
	if !strings.Contains(contentStr, `rule: "HostRegexp(`+"`^miniflux\\\\.`"+`)"`) {
		t.Error("Expected HostRegexp rule for miniflux")
	}

	// Should have priority 200 for app routes
	if !strings.Contains(contentStr, "priority: 200") {
		t.Error("Expected priority 200 for app routes")
	}

	// Check service
	if !strings.Contains(contentStr, `url: "http://localhost:8085"`) {
		t.Error("Expected correct service URL")
	}
}

func TestGenerator_Generate_CustomHeaders(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			CatalogID: "actual-budget",
			Port:      5006,
			IsSystem:  false,
			Routing: &catalog.Routing{
				Headers: map[string]string{
					"Cross-Origin-Opener-Policy":   "same-origin",
					"Cross-Origin-Embedder-Policy": "require-corp",
				},
			},
		},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Check custom headers middleware is applied
	if !strings.Contains(contentStr, "- actual-budget-headers") {
		t.Error("Expected actual-budget-headers middleware in router")
	}

	// Check headers middleware definition
	if !strings.Contains(contentStr, "actual-budget-headers:") {
		t.Error("Expected actual-budget-headers middleware definition")
	}
	if !strings.Contains(contentStr, `Cross-Origin-Opener-Policy: "same-origin"`) {
		t.Error("Expected COOP header")
	}
	if !strings.Contains(contentStr, `Cross-Origin-Embedder-Policy: "require-corp"`) {
		t.Error("Expected COEP header")
	}
}

func TestGenerator_Generate_MultipleApps_Sorted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
		{CatalogID: "actual-budget", Port: 5006, IsSystem: false},
		{CatalogID: "adguard-home", Port: 3080, IsSystem: false},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Apps should be sorted alphabetically.
	actualBudgetIdx := strings.Index(contentStr, "    actual-budget:")
	adguardHomeIdx := strings.Index(contentStr, "    adguard-home:")
	minifluxIdx := strings.Index(contentStr, "    miniflux:")

	if actualBudgetIdx > adguardHomeIdx || adguardHomeIdx > minifluxIdx {
		t.Error("Routers should be sorted alphabetically")
	}
}

func TestGenerator_Generate_AppsWithoutPort_Filtered(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
		{CatalogID: "no-port-app", Port: 0, IsSystem: false},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// App without port should be filtered
	if strings.Contains(contentStr, "no-port-app") {
		t.Error("App without port should be filtered out")
	}

	// App with port should be included
	if !strings.Contains(contentStr, "miniflux") {
		t.Error("App with port should be included")
	}
}

func TestGenerator_Preview(t *testing.T) {
	g := NewGenerator("/nonexistent/path")

	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
	}

	preview := g.Preview(apps)

	if !strings.Contains(preview, "miniflux:") {
		t.Error("Preview should contain router config")
	}
	if !strings.Contains(preview, "# Generated by Bloud") {
		t.Error("Preview should contain header comment")
	}
}

func TestGenerator_Generate_DomainAgnostic(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{CatalogID: "jellyfin", Port: 8096, IsSystem: false},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// HostRegexp matches any domain: jellyfin.localhost, jellyfin.bloud.co, etc.
	if !strings.Contains(contentStr, `rule: "HostRegexp(`+"`^jellyfin\\\\.`"+`)"`) {
		t.Error("Expected HostRegexp rule for jellyfin")
	}
}

// Golden file tests - compare generated output against expected files in testdata/

func loadGoldenFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read golden file %s: %v", path, err)
	}
	return string(content)
}

func TestGolden_EmptyApps(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
	got := g.Preview(nil)
	want := loadGoldenFile(t, "empty.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_BasicApp(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
	}

	got := g.Preview(apps)
	want := loadGoldenFile(t, "basic_app.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_CustomHeaders(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
	apps := []*catalog.App{
		{
			CatalogID: "actual-budget",
			Port:      5006,
			IsSystem:  false,
			Routing: &catalog.Routing{
				Headers: map[string]string{
					"Cross-Origin-Opener-Policy":   "same-origin",
					"Cross-Origin-Embedder-Policy": "require-corp",
				},
			},
		},
	}

	got := g.Preview(apps)
	want := loadGoldenFile(t, "custom_headers.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_MultipleApps(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
		{CatalogID: "actual-budget", Port: 5006, IsSystem: false},
		{CatalogID: "adguard-home", Port: 3080, IsSystem: false},
	}

	got := g.Preview(apps)
	want := loadGoldenFile(t, "multiple_apps.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGenerator_Generate_ForwardAuth(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			CatalogID: "adguard-home",
			Port:      3080,
			IsSystem:  false,
			SSO: catalog.SSO{
				Strategy: "forward-auth",
			},
		},
	}

	g := NewGenerator(configPath)
	g.SetAuthentikEnabled(true)

	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Check router has forwardauth middleware
	if !strings.Contains(contentStr, "- adguard-home-forwardauth") {
		t.Error("Expected adguard-home-forwardauth middleware in router")
	}

	// Check forwardauth middleware definition
	if !strings.Contains(contentStr, "adguard-home-forwardauth:") {
		t.Error("Expected adguard-home-forwardauth middleware definition")
	}
	if !strings.Contains(contentStr, "forwardAuth:") {
		t.Error("Expected forwardAuth config")
	}
	if !strings.Contains(contentStr, `address: "http://localhost:9001/outpost.goauthentik.io/auth/traefik"`) {
		t.Error("Expected Authentik forward auth address")
	}
	if !strings.Contains(contentStr, "trustForwardHeader: true") {
		t.Error("Expected trustForwardHeader")
	}
	if !strings.Contains(contentStr, "- X-authentik-username") {
		t.Error("Expected X-authentik-username in authResponseHeaders")
	}

	// Check outpost router bypasses forward-auth for OAuth callback
	if !strings.Contains(contentStr, "adguard-home-outpost:") {
		t.Error("Expected adguard-home-outpost router for OAuth callback")
	}
	if !strings.Contains(contentStr, "HostRegexp(`^adguard-home\\\\.`) && PathPrefix(`/outpost.goauthentik.io/`)") {
		t.Error("Expected HostRegexp + outpost path prefix in router rule")
	}
	if !strings.Contains(contentStr, "priority: 300") {
		t.Error("Expected priority 300 on outpost router")
	}
	if !strings.Contains(contentStr, "service: authentik-outpost") {
		t.Error("Expected authentik-outpost service reference")
	}
	if !strings.Contains(contentStr, "authentik-outpost:") {
		t.Error("Expected authentik-outpost service definition")
	}
}

func TestGenerator_Generate_ForwardAuth_BypassPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			CatalogID: "navidrome",
			Port:      4533,
			IsSystem:  false,
			SSO: catalog.SSO{
				Strategy:    "forward-auth",
				BypassPaths: []string{"/rest/"},
			},
		},
	}

	g := NewGenerator(configPath)
	g.SetAuthentikEnabled(true)

	if err := g.Generate(apps); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	contentStr := string(content)

	// Bypass router must exist with the right rule and priority
	if !strings.Contains(contentStr, "navidrome-bypass-rest:") {
		t.Error("Expected navidrome-bypass-rest router")
	}
	if !strings.Contains(contentStr, "HostRegexp(`^navidrome\\\\.`) && PathPrefix(`/rest/`)") {
		t.Error("Expected HostRegexp + PathPrefix(/rest/) in bypass router rule")
	}
	if !strings.Contains(contentStr, "priority: 300") {
		t.Error("Expected priority: 300 on bypass router")
	}

	// Bypass router must route to the app service, not the outpost
	if !strings.Contains(contentStr, "service: navidrome") {
		t.Error("Expected bypass router to point to navidrome service")
	}

	// The main router still has forward-auth
	if !strings.Contains(contentStr, "- navidrome-forwardauth") {
		t.Error("Main router should still have forwardauth middleware")
	}
}

func TestGenerator_Generate_ForwardAuth_BypassPaths_AuthentikDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			CatalogID: "navidrome",
			Port:      4533,
			IsSystem:  false,
			SSO: catalog.SSO{
				Strategy:    "forward-auth",
				BypassPaths: []string{"/rest/"},
			},
		},
	}

	g := NewGenerator(configPath)
	// Authentik disabled: no forward-auth active, so bypass routers are unnecessary

	if err := g.Generate(apps); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// No bypass routers emitted when Authentik is off
	if strings.Contains(string(content), "bypass") {
		t.Error("Should not emit bypass routers when Authentik is disabled")
	}
}

func TestGenerator_Generate_ForwardAuth_AuthentikDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			CatalogID: "adguard-home",
			Port:      3080,
			IsSystem:  false,
			SSO: catalog.SSO{
				Strategy: "forward-auth",
			},
		},
	}

	g := NewGenerator(configPath)
	// Don't enable Authentik - should not generate forwardauth middleware

	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Should NOT have forwardauth middleware when Authentik is disabled
	if strings.Contains(contentStr, "forwardauth") {
		t.Error("Should NOT have forwardauth middleware when Authentik is disabled")
	}
}

// Every routable app gets the waiting-page middleware, including a plain app
// with no forward-auth and no custom headers. That is the whole point: the
// Bad Gateway page an app with no middleware used to show is exactly the one
// this replaces.
func TestGenerator_Generate_EveryAppGetsTheLoadingMiddleware(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{CatalogID: "miniflux", Port: 8085, IsSystem: false},
	}

	g := NewGenerator(configPath)
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	contentStr := string(content)

	if !strings.Contains(contentStr, "- miniflux-loading") {
		t.Error("the app router does not reference its waiting-page middleware")
	}
	if !strings.Contains(contentStr, "query: /bloud-loading/miniflux") {
		t.Error("the middleware does not point at this app's own waiting page")
	}
	if !strings.Contains(contentStr, "service: host-agent") {
		t.Error("the middleware does not name the host-agent service")
	}
}

// The waiting page is for one case and one case only: Traefik could not reach
// the app. So the line is drawn by who produced the status, not by how bad it
// looks. 502 and 504 are statuses only a proxy can produce, which is the whole
// reason they can stand in for "the app is not there".
//
// Everything the app produced itself has to reach the visitor. A 500 is the app
// disliking the request. A 503 is the app reporting its own dependency down,
// and Hermes answers one by design when its identity provider cannot vouch for
// the session, keeping the cookie on purpose so an IdP blip does not log every
// signed-in user out. Covering 503 put an endless "re-loading" page over an
// install whose gateway was running, and threw away the one line that named the
// fault.
func TestGenerator_LoadingMiddlewareCoversOnlyTheStatusesAProxyOwns(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
	out := g.Preview([]*catalog.App{{CatalogID: "miniflux", Port: 8085}})

	if !strings.Contains(out, "          - \"502\"\n          - \"504\"\n") {
		t.Error("the waiting page must cover exactly 502 then 504, and nothing between them")
	}
	for _, wrong := range []string{`"503"`, `"502-504"`, `"500"`, `"500-`, `"404"`, `"5xx"`} {
		if strings.Contains(out, wrong) {
			t.Errorf("the waiting page covers %s, which is not a status only a proxy can produce", wrong)
		}
	}
}

// Convergence now runs on a timer. An unconditional atomic write replaces a
// file Traefik watches on every pass: identical bytes, new mtime, so the
// whole dynamic config reloads once a minute forever. A pass with nothing to
// change has to leave the file alone.
func TestGenerator_Generate_SkipsWriteWhenUnchanged(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "apps-routes.yml")
	apps := []*catalog.App{{CatalogID: "jellyfin", Port: 8096}}

	g := NewGenerator(configPath)
	if err := g.Generate(apps); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	first, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Enough of a gap that a rewrite would move the mtime on any filesystem
	// this test runs on.
	time.Sleep(30 * time.Millisecond)

	if err := g.Generate(apps); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	second, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if !first.ModTime().Equal(second.ModTime()) {
		t.Errorf("unchanged config rewrote the file: mtime %v -> %v", first.ModTime(), second.ModTime())
	}
}

// The skip must be a content comparison, not a "the file exists" one: a
// different app set has to land on disk.
func TestGenerator_Generate_RewritesWhenContentDiffers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "apps-routes.yml")

	g := NewGenerator(configPath)
	if err := g.Generate([]*catalog.App{{CatalogID: "jellyfin", Port: 8096}}); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := g.Generate([]*catalog.App{
		{CatalogID: "jellyfin", Port: 8096},
		{CatalogID: "navidrome", Port: 4533},
	}); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if string(after) == string(before) {
		t.Fatal("changed config must be written")
	}
	if !strings.Contains(string(after), "navidrome") {
		t.Errorf("the new app is missing from the rewritten config:\n%s", after)
	}
}
