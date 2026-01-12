package traefikgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

func boolPtr(b bool) *bool {
	return &b
}

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
		{Name: "postgres", Port: 5432, IsSystem: true},
		{Name: "traefik", Port: 8080, IsSystem: true},
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
		{Name: "miniflux", Port: 8085, IsSystem: false},
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

	// Check router
	if !strings.Contains(contentStr, "miniflux-backend:") {
		t.Error("Expected miniflux-backend router")
	}
	if !strings.Contains(contentStr, "rule: \"PathPrefix(`/embed/miniflux`)\"") {
		t.Error("Expected PathPrefix rule for /embed/miniflux")
	}

	// Check middlewares - default should strip prefix
	if !strings.Contains(contentStr, "- miniflux-stripprefix") {
		t.Error("Expected stripprefix middleware")
	}
	if !strings.Contains(contentStr, "- iframe-headers") {
		t.Error("Expected iframe-headers middleware")
	}
	if !strings.Contains(contentStr, "- embed-isolation") {
		t.Error("Expected embed-isolation middleware")
	}

	// Check stripPrefix middleware definition
	if !strings.Contains(contentStr, "miniflux-stripprefix:") {
		t.Error("Expected miniflux-stripprefix middleware definition")
	}
	if !strings.Contains(contentStr, `- "/embed/miniflux"`) {
		t.Error("Expected stripPrefix prefix")
	}

	// Check service
	if !strings.Contains(contentStr, "miniflux:") {
		t.Error("Expected miniflux service")
	}
	if !strings.Contains(contentStr, `url: "http://localhost:8085"`) {
		t.Error("Expected correct service URL")
	}
}

func TestGenerator_Generate_StripPrefixDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			Name:     "miniflux",
			Port:     8085,
			IsSystem: false,
			Routing: &catalog.Routing{
				StripPrefix: boolPtr(false),
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

	// Should NOT have stripprefix middleware
	if strings.Contains(contentStr, "- miniflux-stripprefix") {
		t.Error("Should NOT have stripprefix middleware when disabled")
	}

	// Should NOT define stripprefix middleware
	if strings.Contains(contentStr, "miniflux-stripprefix:") {
		t.Error("Should NOT define stripprefix middleware when disabled")
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

func TestGenerator_Generate_CustomCOEP_SkipsEmbedIsolation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			Name:     "actual-budget",
			Port:     5006,
			IsSystem: false,
			Routing: &catalog.Routing{
				Headers: map[string]string{
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

	// Should NOT have embed-isolation middleware when app defines custom COEP
	// We need to check the router middlewares section specifically
	lines := strings.Split(contentStr, "\n")
	inRouter := false
	hasEmbedIsolation := false
	for _, line := range lines {
		if strings.Contains(line, "actual-budget-backend:") {
			inRouter = true
		}
		if inRouter && strings.Contains(line, "service:") {
			inRouter = false
		}
		if inRouter && strings.Contains(line, "- embed-isolation") {
			hasEmbedIsolation = true
		}
	}

	if hasEmbedIsolation {
		t.Error("Should NOT have embed-isolation middleware when app defines custom COEP")
	}

	// But should still have iframe-headers
	if !strings.Contains(contentStr, "- iframe-headers") {
		t.Error("Should still have iframe-headers middleware")
	}
}

func TestGenerator_Generate_AbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			Name:     "adguard-home",
			Port:     3080,
			IsSystem: false,
			Routing: &catalog.Routing{
				AbsolutePaths: []catalog.AbsolutePath{
					{
						Rule:     "Path(`/install.html`) || Path(`/login.html`)",
						Priority: 97,
					},
					{
						Rule:     "PathPrefix(`/control`)",
						Priority: 97,
					},
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

	// Check main router exists
	if !strings.Contains(contentStr, "adguard-home-backend:") {
		t.Error("Expected main adguard-home-backend router")
	}

	// Check absolute path routers exist
	if !strings.Contains(contentStr, "adguard-home-absolute-0:") {
		t.Error("Expected adguard-home-absolute-0 router")
	}
	if !strings.Contains(contentStr, "adguard-home-absolute-1:") {
		t.Error("Expected adguard-home-absolute-1 router")
	}

	// Check absolute path rules
	if !strings.Contains(contentStr, "rule: \"Path(`/install.html`) || Path(`/login.html`)\"") {
		t.Error("Expected install.html/login.html rule")
	}
	if !strings.Contains(contentStr, "rule: \"PathPrefix(`/control`)\"") {
		t.Error("Expected /control prefix rule")
	}

	// Check priority
	if !strings.Contains(contentStr, "priority: 97") {
		t.Error("Expected priority 97 for absolute path routers")
	}
}

func TestGenerator_Generate_AbsolutePathsWithCustomHeaders(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "apps-routes.yml")

	apps := []*catalog.App{
		{
			Name:     "actual-budget",
			Port:     5006,
			IsSystem: false,
			Routing: &catalog.Routing{
				Headers: map[string]string{
					"Cross-Origin-Embedder-Policy": "require-corp",
				},
				AbsolutePaths: []catalog.AbsolutePath{
					{
						Rule:     "PathPrefix(`/static`)",
						Priority: 99,
					},
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

	// Check absolute path router exists
	if !strings.Contains(contentStr, "actual-budget-absolute-0:") {
		t.Error("Expected actual-budget-absolute-0 router")
	}

	// Absolute path router should also get custom headers middleware
	// Find the absolute router section and check for headers middleware
	lines := strings.Split(contentStr, "\n")
	inAbsoluteRouter := false
	hasCustomHeaders := false
	for _, line := range lines {
		if strings.Contains(line, "actual-budget-absolute-0:") {
			inAbsoluteRouter = true
		}
		if inAbsoluteRouter && strings.Contains(line, "service:") {
			inAbsoluteRouter = false
		}
		if inAbsoluteRouter && strings.Contains(line, "- actual-budget-headers") {
			hasCustomHeaders = true
		}
	}

	if !hasCustomHeaders {
		t.Error("Absolute path router should have custom headers middleware")
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

	// Apps should be sorted alphabetically
	actualBudgetIdx := strings.Index(contentStr, "actual-budget-backend:")
	adguardHomeIdx := strings.Index(contentStr, "adguard-home-backend:")
	minifluxIdx := strings.Index(contentStr, "miniflux-backend:")

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
		{Name: "miniflux", Port: 8085, IsSystem: false},
	}

	preview := g.Preview(apps)

	if !strings.Contains(preview, "miniflux-backend:") {
		t.Error("Preview should contain router config")
	}
	if !strings.Contains(preview, "# Generated by Bloud") {
		t.Error("Preview should contain header comment")
	}
}

func TestShouldStripPrefix(t *testing.T) {
	tests := []struct {
		name     string
		app      *catalog.App
		expected bool
	}{
		{
			name:     "nil routing",
			app:      &catalog.App{},
			expected: true, // default
		},
		{
			name:     "nil stripPrefix",
			app:      &catalog.App{Routing: &catalog.Routing{}},
			expected: true, // default
		},
		{
			name:     "explicit true",
			app:      &catalog.App{Routing: &catalog.Routing{StripPrefix: boolPtr(true)}},
			expected: true,
		},
		{
			name:     "explicit false",
			app:      &catalog.App{Routing: &catalog.Routing{StripPrefix: boolPtr(false)}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldStripPrefix(tt.app)
			if result != tt.expected {
				t.Errorf("shouldStripPrefix() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestHasCustomCOEP(t *testing.T) {
	tests := []struct {
		name     string
		app      *catalog.App
		expected bool
	}{
		{
			name:     "nil routing",
			app:      &catalog.App{},
			expected: false,
		},
		{
			name:     "empty headers",
			app:      &catalog.App{Routing: &catalog.Routing{Headers: map[string]string{}}},
			expected: false,
		},
		{
			name: "other headers only",
			app: &catalog.App{Routing: &catalog.Routing{
				Headers: map[string]string{"X-Custom": "value"},
			}},
			expected: false,
		},
		{
			name: "has COEP",
			app: &catalog.App{Routing: &catalog.Routing{
				Headers: map[string]string{"Cross-Origin-Embedder-Policy": "require-corp"},
			}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasCustomCOEP(tt.app)
			if result != tt.expected {
				t.Errorf("hasCustomCOEP() = %v, want %v", result, tt.expected)
			}
		})
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
		{Name: "miniflux", Port: 8085, IsSystem: false},
	}

	got := g.Preview(apps)
	want := loadGoldenFile(t, "basic_app.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_NoStripPrefix(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
	apps := []*catalog.App{
		{
			Name:     "miniflux",
			Port:     8085,
			IsSystem: false,
			Routing: &catalog.Routing{
				StripPrefix: boolPtr(false),
			},
		},
	}

	got := g.Preview(apps)
	want := loadGoldenFile(t, "no_strip_prefix.golden.yml")

	if got != want {
		t.Errorf("Output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

func TestGolden_CustomHeaders(t *testing.T) {
	g := NewGenerator("/tmp/test.yml")
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
	g := NewGenerator("/tmp/test.yml")
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

	g := NewGenerator(configPath)
	g.SetAuthentikEnabled(true) // Enable Authentik for forward auth

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

