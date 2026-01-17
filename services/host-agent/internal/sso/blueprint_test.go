package sso

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

func TestGenerateOIDCBlueprint(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	app := &catalog.App{
		Name:        "actual-budget",
		DisplayName: "Actual Budget",
		Port:        5006,
		SSO: catalog.SSO{
			Strategy:     "native-oidc",
			CallbackPath: "/openid/callback",
		},
	}

	err := gen.GenerateForApp(app)
	if err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	// Read the generated file
	content, err := os.ReadFile(filepath.Join(dir, "actual-budget.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)
	t.Logf("Generated blueprint:\n%s", contentStr)

	// Verify redirect URIs - both embed path and root-level callback
	expectedEmbedRedirectURI := "http://localhost:8080/embed/actual-budget/openid/callback"
	if !strings.Contains(contentStr, expectedEmbedRedirectURI) {
		t.Errorf("Expected embed redirect URI %s not found in blueprint", expectedEmbedRedirectURI)
	}

	expectedRootRedirectURI := "http://localhost:8080/openid/callback"
	if !strings.Contains(contentStr, expectedRootRedirectURI) {
		t.Errorf("Expected root-level redirect URI %s not found in blueprint", expectedRootRedirectURI)
	}

	// Verify client ID
	if !strings.Contains(contentStr, "actual-budget-client") {
		t.Error("Expected client ID 'actual-budget-client' not found")
	}

	// Verify client secret
	if !strings.Contains(contentStr, "actual-budget-secret-change-in-production") {
		t.Error("Expected client secret not found")
	}
}

func TestGenerateForwardAuthBlueprint(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	app := &catalog.App{
		Name:        "adguard-home",
		DisplayName: "AdGuard Home",
		Port:        3080,
		SSO: catalog.SSO{
			Strategy: "forward-auth",
		},
	}

	err := gen.GenerateForApp(app)
	if err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	// Read the generated file
	content, err := os.ReadFile(filepath.Join(dir, "adguard-home.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)
	t.Logf("Generated blueprint:\n%s", contentStr)

	// Verify it's a proxy provider
	if !strings.Contains(contentStr, "proxyprovider") {
		t.Error("Expected proxyprovider model not found")
	}

	// Verify mode
	if !strings.Contains(contentStr, "mode: forward_single") {
		t.Error("Expected forward_single mode not found")
	}

	// Verify external host - should be root URL (not app-specific embed path)
	// because the callback /outpost.goauthentik.io/callback is routed at root level
	if !strings.Contains(contentStr, `external_host: "http://localhost:8080"`) {
		t.Error("Expected external_host to be root URL")
	}
}

func TestDeleteBlueprint(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	// Create a blueprint file first
	app := &catalog.App{
		Name:        "test-app",
		DisplayName: "Test App",
		Port:        8080,
		SSO: catalog.SSO{
			Strategy:     "native-oidc",
			CallbackPath: "/callback",
		},
	}

	err := gen.GenerateForApp(app)
	if err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	// Verify file exists
	blueprintPath := filepath.Join(dir, "test-app.yaml")
	if _, err := os.Stat(blueprintPath); os.IsNotExist(err) {
		t.Fatal("Blueprint file was not created")
	}

	// Delete the blueprint
	err = gen.DeleteBlueprint("test-app")
	if err != nil {
		t.Fatalf("DeleteBlueprint failed: %v", err)
	}

	// Verify file is deleted
	if _, err := os.Stat(blueprintPath); !os.IsNotExist(err) {
		t.Error("Blueprint file was not deleted")
	}
}

func TestDeleteBlueprint_NonExistent(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	// Deleting non-existent file should not error
	err := gen.DeleteBlueprint("nonexistent-app")
	if err != nil {
		t.Errorf("DeleteBlueprint should not error for non-existent file: %v", err)
	}
}

func TestGenerateOutpostBlueprint(t *testing.T) {
	dir := t.TempDir()

	// Use different URLs for baseURL and authentikURL to test the distinction
	// baseURL = main app URL, authentikURL = Authentik SSO URL for OAuth redirects
	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",        // baseURL - where apps are served
		"http://auth.localhost:8080",   // authentikURL - where Authentik is served
		dir,
	)

	providers := []ForwardAuthProvider{
		{DisplayName: "App One"},
		{DisplayName: "App Two"},
	}

	err := gen.GenerateOutpostBlueprint(providers)
	if err != nil {
		t.Fatalf("GenerateOutpostBlueprint failed: %v", err)
	}

	// Read the generated file
	content, err := os.ReadFile(filepath.Join(dir, "bloud-outpost.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)
	t.Logf("Generated blueprint:\n%s", contentStr)

	// Verify it references the embedded outpost
	if !strings.Contains(contentStr, "authentik Embedded Outpost") {
		t.Error("Expected 'authentik Embedded Outpost' identifier not found")
	}

	// Verify provider references using !Find syntax
	if !strings.Contains(contentStr, `"App One Proxy Provider"`) {
		t.Error("Expected App One provider reference not found")
	}
	if !strings.Contains(contentStr, `"App Two Proxy Provider"`) {
		t.Error("Expected App Two provider reference not found")
	}

	// Verify config uses authentikURL (NOT baseURL) for OAuth redirects
	// This is critical: the outpost needs to redirect to auth.localhost, not localhost
	if !strings.Contains(contentStr, `authentik_host: "http://auth.localhost:8080"`) {
		t.Error("Expected authentik_host to use authentikURL (auth.localhost:8080), not baseURL")
	}
	if !strings.Contains(contentStr, `authentik_host_browser: "http://auth.localhost:8080"`) {
		t.Error("Expected authentik_host_browser to use authentikURL (auth.localhost:8080), not baseURL")
	}
}

// TestGenerateOutpostBlueprint_UsesAuthentikURL verifies the embedded outpost
// uses authentikURL for OAuth redirects (not baseURL).
func TestGenerateOutpostBlueprint_UsesAuthentikURL(t *testing.T) {
	dir := t.TempDir()

	baseURL := "http://localhost:8080"
	authentikURL := "http://auth.localhost:8080"

	gen := NewBlueprintGenerator("test-secret", baseURL, authentikURL, dir)

	err := gen.GenerateOutpostBlueprint([]ForwardAuthProvider{{DisplayName: "Test App"}})
	if err != nil {
		t.Fatalf("GenerateOutpostBlueprint failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "bloud-outpost.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	contentStr := string(content)

	if !strings.Contains(contentStr, `authentik_host: "http://auth.localhost:8080"`) {
		t.Error("authentik_host should use authentikURL")
	}
	if !strings.Contains(contentStr, `authentik_host_browser: "http://auth.localhost:8080"`) {
		t.Error("authentik_host_browser should use authentikURL")
	}
}

func TestGenerateOutpostBlueprint_NoProviders(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	// Create a blueprint file first
	err := gen.GenerateOutpostBlueprint([]ForwardAuthProvider{{DisplayName: "Test"}})
	if err != nil {
		t.Fatalf("GenerateOutpostBlueprint failed: %v", err)
	}

	blueprintPath := filepath.Join(dir, "bloud-outpost.yaml")
	if _, err := os.Stat(blueprintPath); os.IsNotExist(err) {
		t.Fatal("Blueprint file was not created")
	}

	// Call with empty providers - should remove the file
	err = gen.GenerateOutpostBlueprint([]ForwardAuthProvider{})
	if err != nil {
		t.Fatalf("GenerateOutpostBlueprint with empty providers failed: %v", err)
	}

	// Verify file is removed
	if _, err := os.Stat(blueprintPath); !os.IsNotExist(err) {
		t.Error("Blueprint file should be removed when no providers")
	}
}

func TestGenerateLDAPBlueprint(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	app := &catalog.App{
		Name:        "jellyfin",
		DisplayName: "Jellyfin",
		Port:        8096,
		SSO: catalog.SSO{
			Strategy: "ldap",
		},
	}

	err := gen.GenerateForApp(app)
	if err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	// Read the generated file
	content, err := os.ReadFile(filepath.Join(dir, "jellyfin.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)
	t.Logf("Generated blueprint:\n%s", contentStr)

	// Verify it creates groups
	if !strings.Contains(contentStr, "jellyfin-users") {
		t.Error("Expected jellyfin-users group not found")
	}
	if !strings.Contains(contentStr, "jellyfin-admins") {
		t.Error("Expected jellyfin-admins group not found")
	}

	// Verify it creates an application entry
	if !strings.Contains(contentStr, "authentik_core.application") {
		t.Error("Expected application model not found")
	}

	// Verify launch URL
	if !strings.Contains(contentStr, "http://localhost:8080/embed/jellyfin") {
		t.Error("Expected launch URL not found")
	}
}

func TestGenerateLDAPOutpostBlueprint(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	apps := []LDAPApp{
		{Name: "jellyfin", DisplayName: "Jellyfin"},
	}

	err := gen.GenerateLDAPOutpostBlueprint(apps, "test-bind-password")
	if err != nil {
		t.Fatalf("GenerateLDAPOutpostBlueprint failed: %v", err)
	}

	// Read the generated file
	content, err := os.ReadFile(filepath.Join(dir, "bloud-ldap.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	contentStr := string(content)
	t.Logf("Generated blueprint:\n%s", contentStr)

	// Verify LDAP provider
	if !strings.Contains(contentStr, "authentik_providers_ldap.ldapprovider") {
		t.Error("Expected LDAP provider model not found")
	}
	if !strings.Contains(contentStr, "Bloud LDAP Provider") {
		t.Error("Expected LDAP provider name not found")
	}

	// Verify service account
	if !strings.Contains(contentStr, "ldap-service") {
		t.Error("Expected ldap-service user not found")
	}
	if !strings.Contains(contentStr, "service_account") {
		t.Error("Expected service_account type not found")
	}

	// Verify bind password token
	if !strings.Contains(contentStr, "test-bind-password") {
		t.Error("Expected bind password not found")
	}

	// Verify LDAP outpost
	if !strings.Contains(contentStr, "Bloud LDAP Outpost") {
		t.Error("Expected LDAP outpost name not found")
	}
	if !strings.Contains(contentStr, "type: ldap") {
		t.Error("Expected outpost type ldap not found")
	}
}

func TestGenerateLDAPOutpostBlueprint_NoApps(t *testing.T) {
	dir := t.TempDir()

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
	)

	// Create a blueprint file first
	err := gen.GenerateLDAPOutpostBlueprint([]LDAPApp{{Name: "test", DisplayName: "Test"}}, "password")
	if err != nil {
		t.Fatalf("GenerateLDAPOutpostBlueprint failed: %v", err)
	}

	blueprintPath := filepath.Join(dir, "bloud-ldap.yaml")
	if _, err := os.Stat(blueprintPath); os.IsNotExist(err) {
		t.Fatal("Blueprint file was not created")
	}

	// Call with empty apps - should remove the file
	err = gen.GenerateLDAPOutpostBlueprint([]LDAPApp{}, "password")
	if err != nil {
		t.Fatalf("GenerateLDAPOutpostBlueprint with empty apps failed: %v", err)
	}

	// Verify file is removed
	if _, err := os.Stat(blueprintPath); !os.IsNotExist(err) {
		t.Error("Blueprint file should be removed when no LDAP apps")
	}
}

func TestGetLDAPBindPassword(t *testing.T) {
	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
		t.TempDir(),
	)

	password := gen.GetLDAPBindPassword()
	if password == "" {
		t.Error("Expected non-empty LDAP bind password")
	}
}
