// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManager_PublishedAppSecrets pins the store half of integration bindings:
// a credential an app generates for itself is kept under a name it chooses and
// survives a reload. A published credential reaches a consumer through a
// binding, never through a container's environment.
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

	// No per-app env file is written at all. The store used to generate one per
	// app on every save; nothing mounts it, and a published credential reaches a
	// consumer through a binding, never through a container's environment. Pin
	// the absence so the writer cannot come back looking load-bearing.
	if entries, err := os.ReadDir(tmpDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".env") {
				t.Errorf("unexpected generated env file %q: published credentials travel through bindings, not container environments", e.Name())
			}
		}
	}

	m2 := NewManager(secretsPath)
	if err := m2.Load(); err != nil {
		t.Fatalf("failed to reload: %v", err)
	}
	if got := m2.GetAppSecret("sonarr", "apiKey"); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("after reload: GetAppSecret(apiKey) = %q, want the published key", got)
	}
}

// TestManager_AppContractValues pins the runtime-published value channel: a
// provider stores a non-secret fact its own app minted, scoped by contract so two
// contracts sharing a value key cannot collide, and reads it back across a
// reload. Re-publishing the same value is a no-op, because a configurator
// publishes on every reconciliation pass.
func TestManager_AppContractValues(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if err := m.SetAppContractValue("affine", "mcp", "path", "/api/workspaces/ws-1/mcp"); err != nil {
		t.Fatalf("failed to publish contract value: %v", err)
	}
	if got := m.GetAppContractValue("affine", "mcp", "path"); got != "/api/workspaces/ws-1/mcp" {
		t.Errorf("GetAppContractValue(mcp/path) = %q, want the published path", got)
	}
	// Scoped by contract: the same key under another contract is a different value.
	if got := m.GetAppContractValue("affine", "modelSource", "path"); got != "" {
		t.Errorf("contract values leaked across the contract scope: modelSource/path = %q", got)
	}
	// An unpublished provider/key reads as empty, the same "not ready" signal a
	// published secret gives.
	if got := m.GetAppContractValue("no-such-app", "mcp", "path"); got != "" {
		t.Errorf("GetAppContractValue for an unknown provider = %q, want empty", got)
	}

	// A second write of the same value must not rewrite the file.
	before, err := os.Stat(secretsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := m.SetAppContractValue("affine", "mcp", "path", "/api/workspaces/ws-1/mcp"); err != nil {
		t.Fatalf("re-publishing the same value failed: %v", err)
	}
	after, err := os.Stat(secretsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("re-publishing an unchanged contract value rewrote secrets.json")
	}
	// A changed value does land.
	if err := m.SetAppContractValue("affine", "mcp", "path", "/api/workspaces/ws-2/mcp"); err != nil {
		t.Fatalf("failed to update contract value: %v", err)
	}
	if got := m.GetAppContractValue("affine", "mcp", "path"); got != "/api/workspaces/ws-2/mcp" {
		t.Errorf("after update: GetAppContractValue = %q, want the new path", got)
	}

	m2 := NewManager(secretsPath)
	if err := m2.Load(); err != nil {
		t.Fatalf("failed to reload: %v", err)
	}
	if got := m2.GetAppContractValue("affine", "mcp", "path"); got != "/api/workspaces/ws-2/mcp" {
		t.Errorf("after reload: GetAppContractValue = %q, want the persisted path", got)
	}
	// GetAllSecrets hands out a copy, not the live nested maps.
	all := m2.GetAllSecrets()
	all.AppSecrets["affine"].PublishedValues["mcp"]["path"] = "mutated"
	if got := m2.GetAppContractValue("affine", "mcp", "path"); got != "/api/workspaces/ws-2/mcp" {
		t.Errorf("GetAllSecrets aliased the published-value map: mutating the copy changed the store to %q", got)
	}
}

// TestManager_SetAppSecretIsIdempotent pins the no-op: configurators publish on
// every reconciliation, so re-writing a value it already holds must not touch
// the file.
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
