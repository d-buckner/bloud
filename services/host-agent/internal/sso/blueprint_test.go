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

	gen := NewBlueprintGenerator(
		"test-secret",
		"http://localhost:8080",
		"http://localhost:8080",
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

	// Verify config includes authentik_host and authentik_host_browser
	if !strings.Contains(contentStr, `authentik_host: "http://localhost:8080"`) {
		t.Error("Expected authentik_host config not found")
	}
	if !strings.Contains(contentStr, `authentik_host_browser: "http://localhost:8080"`) {
		t.Error("Expected authentik_host_browser config not found")
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
