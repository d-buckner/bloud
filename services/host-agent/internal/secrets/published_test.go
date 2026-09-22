// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManager_PublishedAppSecrets pins the store half of integration bindings:
// a credential an app generates for itself is kept under a name it chooses,
// survives a reload, and is not written into the app's env file (a published
// credential reaches a consumer through a binding, never through a container's
// environment).
func TestManager_PublishedAppSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if err := m.SetAppSecret("sonarr", "apiKey", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("failed to publish app secret: %v", err)
	}
	if got := m.GetAppSecret("sonarr", "apiKey"); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("GetAppSecret(apiKey) = %q, want the published key", got)
	}
	// An app that never published a name has no value for it.
	if got := m.GetAppSecret("sonarr", "apiToken"); got != "" {
		t.Errorf("GetAppSecret(apiToken) = %q, want empty for an undeclared name", got)
	}
	// The typed slots and the published bag do not shadow each other.
	if err := m.SetAppSecret("sonarr", "adminPassword", "typed"); err != nil {
		t.Fatalf("failed to set a typed app secret: %v", err)
	}
	if got := m.GetAppSecret("sonarr", "adminPassword"); got != "typed" {
		t.Errorf("GetAppSecret(adminPassword) = %q, want the typed value", got)
	}
	if got := m.GetAppSecret("sonarr", "apiKey"); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("the published bag lost its value when a typed slot was written: %q", got)
	}

	envFile := filepath.Join(tmpDir, "sonarr.env")
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("reading %s: %v", envFile, err)
	}
	if !strings.Contains(string(env), "ADMIN_PASSWORD=typed\n") {
		t.Errorf("%s does not carry the typed admin password: %q", envFile, env)
	}
	if got := string(env); strings.Contains(got, "0123456789abcdef0123456789abcdef") {
		t.Errorf("%s leaked a published credential into the container environment: %q", envFile, got)
	}

	m2 := NewManager(secretsPath)
	if err := m2.Load(); err != nil {
		t.Fatalf("failed to reload: %v", err)
	}
	if got := m2.GetAppSecret("sonarr", "apiKey"); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("after reload: GetAppSecret(apiKey) = %q, want the published key", got)
	}
}

// TestManager_SetAppSecretIsIdempotent pins the no-op: configurators publish on
// every reconciliation, so re-writing a value it already holds must not touch
// the file (which would rewrite every generated env file with it).
func TestManager_SetAppSecretIsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}
	if err := m.SetAppSecret("sonarr", "apiKey", "key"); err != nil {
		t.Fatalf("failed to publish app secret: %v", err)
	}

	before, err := os.Stat(secretsPath)
	if err != nil {
		t.Fatalf("stat secrets file: %v", err)
	}
	if err := m.SetAppSecret("sonarr", "apiKey", "key"); err != nil {
		t.Fatalf("re-publishing the same value failed: %v", err)
	}
	after, err := os.Stat(secretsPath)
	if err != nil {
		t.Fatalf("stat secrets file: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf("re-publishing an unchanged value rewrote the secrets file (%v → %v, %d → %d bytes)",
			before.ModTime(), after.ModTime(), before.Size(), after.Size())
	}
}

// TestManager_GetAllSecretsCopiesPublished pins that the snapshot callers get
// cannot mutate the live store through the published map.
func TestManager_GetAllSecretsCopiesPublished(t *testing.T) {
	tmpDir := t.TempDir()
	m := NewManager(filepath.Join(tmpDir, "secrets.json"))
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}
	if err := m.SetAppSecret("sonarr", "apiKey", "original"); err != nil {
		t.Fatalf("failed to publish app secret: %v", err)
	}

	snapshot := m.GetAllSecrets()
	snapshot.AppSecrets["sonarr"].Published["apiKey"] = "mutated"

	if got := m.GetAppSecret("sonarr", "apiKey"); got != "original" {
		t.Errorf("mutating the snapshot changed the store: GetAppSecret(apiKey) = %q", got)
	}
}
