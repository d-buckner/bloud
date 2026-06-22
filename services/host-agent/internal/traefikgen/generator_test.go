package traefikgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

func TestGenerator_Generate_EmptyApps(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	g := NewGenerator(configPath, "localhost")
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
		{Name: "postgres", Port: 5432, IsSystem: true},
		{Name: "traefik", Port: 8080, IsSystem: true},
	}

	g := NewGenerator(configPath, "localhost")
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
		{Name: "miniflux", Port: 8085, IsSystem: false},
	}

	g := NewGenerator(configPath, "localhost")
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Check router uses Host rule
	if !strings.Contains(contentStr, "miniflux:") {
		t.Error("Expected miniflux router")
	}
	if !strings.Contains(contentStr, `rule: "Host(` + "`miniflux.localhost`" + `)"`) {
		t.Error("Expected Host rule for miniflux.localhost")
	}

	// Should NOT have priority (Host rules are unambiguous)
	if strings.Contains(contentStr, "priority:") {
		t.Error("Should NOT have priority for Host-based routes")
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
			Name:     "actual-budget",
			Port:     5006,
			IsSystem: false,
			Routing: &catalog.Routing{
				Headers: map[string]string{
					"Cross-Origin-Opener-Policy":   "same-origin",
					"Cross-Origin-Embedder-Policy": "require-corp",
				},
			},
		},
	}

	g := NewGenerator(configPath, "localhost")
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
		{Name: "miniflux", Port: 8085, IsSystem: false},
		{Name: "actual-budget", Port: 5006, IsSystem: false},
		{Name: "adguard-home", Port: 3080, IsSystem: false},
	}

	g := NewGenerator(configPath, "localhost")
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Apps should be sorted alphabetically (router names no longer have -backend suffix)
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
		{Name: "miniflux", Port: 8085, IsSystem: false},
		{Name: "no-port-app", Port: 0, IsSystem: false},
	}

	g := NewGenerator(configPath, "localhost")
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
	g := NewGenerator("/nonexistent/path", "localhost")

	apps := []*catalog.App{
		{Name: "miniflux", Port: 8085, IsSystem: false},
	}

	preview := g.Preview(apps)

	if !strings.Contains(preview, "miniflux:") {
		t.Error("Preview should contain router config")
	}
	if !strings.Contains(preview, "# Generated by Bloud") {
		t.Error("Preview should contain header comment")
	}
}

func TestGenerator_Generate_CustomBaseDomain(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{Name: "jellyfin", Port: 8096, IsSystem: false},
	}

	g := NewGenerator(configPath, "bloud.local")
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	if !strings.Contains(contentStr, `rule: "Host(`+"`jellyfin.bloud.local`"+`)"`) {
		t.Error("Expected Host rule with custom base domain")
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
	g := NewGenerator("/tmp/test.yml", "localhost")
	got := g.Preview(nil)
	want := loadGoldenFile(t, "empty.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_BasicApp(t *testing.T) {
	g := NewGenerator("/tmp/test.yml", "localhost")
	apps := []*catalog.App{
		{Name: "miniflux", Port: 8085, IsSystem: false},
	}

	got := g.Preview(apps)
	want := loadGoldenFile(t, "basic_app.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_CustomHeaders(t *testing.T) {
	g := NewGenerator("/tmp/test.yml", "localhost")
	apps := []*catalog.App{
		{
			Name:     "actual-budget",
			Port:     5006,
			IsSystem: false,
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
	g := NewGenerator("/tmp/test.yml", "localhost")
	apps := []*catalog.App{
		{Name: "miniflux", Port: 8085, IsSystem: false},
		{Name: "actual-budget", Port: 5006, IsSystem: false},
		{Name: "adguard-home", Port: 3080, IsSystem: false},
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
			Name:     "adguard-home",
			Port:     3080,
			IsSystem: false,
			SSO: catalog.SSO{
				Strategy: "forward-auth",
			},
		},
	}

	g := NewGenerator(configPath, "localhost")
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
	if !strings.Contains(contentStr, "PathPrefix(`/outpost.goauthentik.io/`)") {
		t.Error("Expected outpost path prefix in router rule")
	}
	if !strings.Contains(contentStr, "service: authentik-outpost") {
		t.Error("Expected authentik-outpost service reference")
	}
	if !strings.Contains(contentStr, "authentik-outpost:") {
		t.Error("Expected authentik-outpost service definition")
	}
}

func TestGenerator_Generate_ForwardAuth_AuthentikDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			Name:     "adguard-home",
			Port:     3080,
			IsSystem: false,
			SSO: catalog.SSO{
				Strategy: "forward-auth",
			},
		},
	}

	g := NewGenerator(configPath, "localhost")
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

func TestGenerator_Generate_NoMiddlewaresSection_WhenNoneNeeded(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{Name: "miniflux", Port: 8085, IsSystem: false},
	}

	g := NewGenerator(configPath, "localhost")
	err := g.Generate(apps)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)

	// Should NOT have middlewares section when no app needs one
	if strings.Contains(contentStr, "middlewares:") {
		t.Error("Should NOT have middlewares section when no app needs middleware")
	}
}
