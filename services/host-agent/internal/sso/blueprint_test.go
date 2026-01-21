package sso

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
)

// testBlueprintGenerator creates a BlueprintGenerator for testing
func testBlueprintGenerator(t *testing.T, dir string) *BlueprintGenerator {
	return NewBlueprintGenerator(
		"test-secret",
		"test-ldap-password",
		"http://localhost:8080",
		"http://localhost:8080",
		dir,
		nil, // No secrets manager for tests
	)
}

func TestGenerateOIDCBlueprint(t *testing.T) {
	dir := t.TempDir()
	gen := testBlueprintGenerator(t, dir)

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

	// Verify client secret is derived (not the old static pattern)
	// HKDF-derived secrets are base64-encoded, so they should have alphanumeric/+/= chars
	if strings.Contains(contentStr, "actual-budget-secret-change-in-production") {
		t.Error("Client secret should be derived, not static pattern")
	}
	if !strings.Contains(contentStr, "client_secret:") {
		t.Error("Expected client_secret field not found")
	}
}

func TestGenerateForwardAuthBlueprint(t *testing.T) {
	dir := t.TempDir()
	gen := testBlueprintGenerator(t, dir)

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
	gen := testBlueprintGenerator(t, dir)

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
	gen := testBlueprintGenerator(t, dir)

	// Deleting non-existent file should not error
	err := gen.DeleteBlueprint("nonexistent-app")
	if err != nil {
		t.Errorf("DeleteBlueprint should not error for non-existent file: %v", err)
	}
}

func TestGenerateOutpostBlueprint(t *testing.T) {
	dir := t.TempDir()

	// With root-level Authentik paths, baseURL and authentikURL are the same
	// Authentik is accessed at /application/, /flows/, etc. on the same origin
	gen := testBlueprintGenerator(t, dir)

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

	// Verify config uses authentikURL for OAuth redirects
	// With root-level paths, this is the same as baseURL (no /auth prefix)
	if !strings.Contains(contentStr, `authentik_host: "http://localhost:8080"`) {
		t.Error("Expected authentik_host to use authentikURL (localhost:8080)")
	}
	if !strings.Contains(contentStr, `authentik_host_browser: "http://localhost:8080"`) {
		t.Error("Expected authentik_host_browser to use authentikURL (localhost:8080)")
	}
}

// TestGenerateOutpostBlueprint_UsesAuthentikURL verifies the embedded outpost
// uses authentikURL for OAuth redirects.
func TestGenerateOutpostBlueprint_UsesAuthentikURL(t *testing.T) {
	dir := t.TempDir()

	// With root-level Authentik paths, both URLs are the same
	gen := testBlueprintGenerator(t, dir)

	err := gen.GenerateOutpostBlueprint([]ForwardAuthProvider{{DisplayName: "Test App"}})
	if err != nil {
		t.Fatalf("GenerateOutpostBlueprint failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "bloud-outpost.yaml"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	contentStr := string(content)

	if !strings.Contains(contentStr, `authentik_host: "http://localhost:8080"`) {
		t.Error("authentik_host should use authentikURL")
	}
	if !strings.Contains(contentStr, `authentik_host_browser: "http://localhost:8080"`) {
		t.Error("authentik_host_browser should use authentikURL")
	}
}

func TestGenerateOutpostBlueprint_NoProviders(t *testing.T) {
	dir := t.TempDir()
	gen := testBlueprintGenerator(t, dir)

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
	gen := testBlueprintGenerator(t, dir)

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
	gen := testBlueprintGenerator(t, dir)

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
	gen := testBlueprintGenerator(t, dir)

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
	gen := testBlueprintGenerator(t, t.TempDir())

	password := gen.GetLDAPBindPassword()
	if password != "test-ldap-password" {
		t.Errorf("Expected 'test-ldap-password', got '%s'", password)
	}
}

func TestDeriveSecret_Deterministic(t *testing.T) {
	// Same inputs should produce same output
	secret1 := deriveSecret("master-secret", "app-name", 32)
	secret2 := deriveSecret("master-secret", "app-name", 32)

	if secret1 != secret2 {
		t.Error("deriveSecret should be deterministic")
	}
}

func TestDeriveSecret_UniquePerApp(t *testing.T) {
	// Different app names should produce different secrets
	secretA := deriveSecret("master-secret", "app-a", 32)
	secretB := deriveSecret("master-secret", "app-b", 32)

	if secretA == secretB {
		t.Error("Different apps should have different secrets")
	}
}

func TestDeriveSecret_UniquePerHost(t *testing.T) {
	// Different host secrets should produce different secrets
	secret1 := deriveSecret("host-secret-1", "app-name", 32)
	secret2 := deriveSecret("host-secret-2", "app-name", 32)

	if secret1 == secret2 {
		t.Error("Different host secrets should produce different app secrets")
	}
}

func TestDeriveSecret_EmptyMaster(t *testing.T) {
	// Empty master should use fallback
	secret := deriveSecret("", "app-name", 32)

	if secret == "" {
		t.Error("Should return fallback for empty master")
	}
	if !strings.Contains(secret, "app-name") {
		t.Error("Fallback should contain context")
	}
}

func TestDeriveSecret_NoPadding(t *testing.T) {
	// Derived secrets should not contain '=' padding characters.
	// The '=' character causes issues with openid-client which URL-encodes
	// credentials per RFC 6749 before base64 encoding for Basic auth.
	// Authentik doesn't URL-decode after base64 decoding, causing auth failures.
	secret := deriveSecret("test-master-secret", "oauth-client-secret:actual-budget", 32)

	if strings.Contains(secret, "=") {
		t.Errorf("Derived secret should not contain '=' padding, got: %s", secret)
	}

	// Also verify it's a valid base64 URL-safe string (alphanumeric, -, _)
	for _, c := range secret {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			t.Errorf("Secret contains invalid character '%c', should only contain URL-safe base64 chars", c)
		}
	}
}
