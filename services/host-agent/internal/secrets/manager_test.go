package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManager_GeneratesOnFirstLoad(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// Verify secrets were generated
	if m.GetPostgresPassword() == "" {
		t.Error("postgresPassword not generated")
	}
	if m.GetAuthentikSecretKey() == "" {
		t.Error("authentikSecretKey not generated")
	}
	if m.GetAuthentikBootstrapPassword() == "" {
		t.Error("authentikBootstrapPassword not generated")
	}
	if m.GetAuthentikBootstrapToken() == "" {
		t.Error("authentikBootstrapToken not generated")
	}
	if m.GetLDAPOutpostToken() == "" {
		t.Error("ldapOutpostToken not generated")
	}
	if m.GetLDAPBindPassword() == "" {
		t.Error("ldapBindPassword not generated")
	}
	if m.GetSSOHostSecret() == "" {
		t.Error("ssoHostSecret not generated")
	}

	// Verify file was created
	if _, err := os.Stat(secretsPath); os.IsNotExist(err) {
		t.Error("secrets file not created")
	}
}

func TestManager_PersistsSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	// Generate secrets
	m1 := NewManager(secretsPath)
	if err := m1.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	origPassword := m1.GetPostgresPassword()
	origSecretKey := m1.GetAuthentikSecretKey()

	// Load again with a new manager
	m2 := NewManager(secretsPath)
	if err := m2.Load(); err != nil {
		t.Fatalf("failed to load second time: %v", err)
	}

	// Verify secrets are the same
	if m2.GetPostgresPassword() != origPassword {
		t.Error("postgresPassword changed on reload")
	}
	if m2.GetAuthentikSecretKey() != origSecretKey {
		t.Error("authentikSecretKey changed on reload")
	}
}

func TestManager_SecretsAreUnique(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// All secrets should be different
	secrets := []string{
		m.GetPostgresPassword(),
		m.GetAuthentikSecretKey(),
		m.GetAuthentikBootstrapPassword(),
		m.GetAuthentikBootstrapToken(),
		m.GetLDAPOutpostToken(),
		m.GetLDAPBindPassword(),
		m.GetSSOHostSecret(),
	}

	seen := make(map[string]bool)
	for i, s := range secrets {
		if seen[s] {
			t.Errorf("secret at index %d is a duplicate", i)
		}
		seen[s] = true
	}
}

func TestManager_SecretsHaveCorrectLength(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	tests := []struct {
		name     string
		secret   string
		minLen   int
	}{
		{"postgresPassword", m.GetPostgresPassword(), 32},
		{"authentikSecretKey", m.GetAuthentikSecretKey(), 64},
		{"authentikBootstrapPassword", m.GetAuthentikBootstrapPassword(), 32},
		{"authentikBootstrapToken", m.GetAuthentikBootstrapToken(), 48},
		{"ldapOutpostToken", m.GetLDAPOutpostToken(), 48},
		{"ldapBindPassword", m.GetLDAPBindPassword(), 32},
		{"ssoHostSecret", m.GetSSOHostSecret(), 64},
	}

	for _, tc := range tests {
		if len(tc.secret) < tc.minLen {
			t.Errorf("%s: expected length >= %d, got %d", tc.name, tc.minLen, len(tc.secret))
		}
	}
}

func TestManager_AppSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// Set an app secret
	if err := m.SetAppSecret("miniflux", "oauthClientSecret", "test-secret-123"); err != nil {
		t.Fatalf("failed to set app secret: %v", err)
	}

	// Verify it can be retrieved
	if got := m.GetAppSecret("miniflux", "oauthClientSecret"); got != "test-secret-123" {
		t.Errorf("expected 'test-secret-123', got '%s'", got)
	}

	// Verify persistence
	m2 := NewManager(secretsPath)
	if err := m2.Load(); err != nil {
		t.Fatalf("failed to load second time: %v", err)
	}

	if got := m2.GetAppSecret("miniflux", "oauthClientSecret"); got != "test-secret-123" {
		t.Errorf("after reload: expected 'test-secret-123', got '%s'", got)
	}
}

func TestManager_GenerateAppAdminPassword(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// Generate password for app
	password1, err := m.GenerateAppAdminPassword("jellyseerr")
	if err != nil {
		t.Fatalf("failed to generate password: %v", err)
	}

	if len(password1) < 24 {
		t.Errorf("password too short: %d", len(password1))
	}

	// Calling again should return the same password
	password2, err := m.GenerateAppAdminPassword("jellyseerr")
	if err != nil {
		t.Fatalf("failed to get password second time: %v", err)
	}

	if password1 != password2 {
		t.Error("password changed on second call")
	}

	// Verify via GetAppSecret
	if got := m.GetAppSecret("jellyseerr", "adminPassword"); got != password1 {
		t.Errorf("GetAppSecret returned different value: %s", got)
	}
}

func TestManager_DeleteAppSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// Set some secrets
	if err := m.SetAppSecret("testapp", "adminPassword", "pass123"); err != nil {
		t.Fatalf("failed to set secret: %v", err)
	}
	if err := m.SetAppSecret("testapp", "oauthClientSecret", "oauth123"); err != nil {
		t.Fatalf("failed to set secret: %v", err)
	}

	// Delete
	if err := m.DeleteAppSecrets("testapp"); err != nil {
		t.Fatalf("failed to delete secrets: %v", err)
	}

	// Verify deleted
	if got := m.GetAppSecret("testapp", "adminPassword"); got != "" {
		t.Errorf("expected empty string, got '%s'", got)
	}
	if got := m.GetAppSecret("testapp", "oauthClientSecret"); got != "" {
		t.Errorf("expected empty string, got '%s'", got)
	}

	// Verify persisted
	m2 := NewManager(secretsPath)
	if err := m2.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}
	if got := m2.GetAppSecret("testapp", "adminPassword"); got != "" {
		t.Error("secret not deleted from file")
	}
}

func TestManager_GetAllSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if err := m.SetAppSecret("app1", "adminPassword", "pass1"); err != nil {
		t.Fatalf("failed to set secret: %v", err)
	}

	all := m.GetAllSecrets()
	if all == nil {
		t.Fatal("GetAllSecrets returned nil")
	}

	if all.PostgresPassword != m.GetPostgresPassword() {
		t.Error("PostgresPassword mismatch")
	}

	if all.AppSecrets["app1"].AdminPassword != "pass1" {
		t.Error("AppSecrets not included")
	}

	// Verify it's a copy (modification doesn't affect original)
	all.PostgresPassword = "modified"
	if m.GetPostgresPassword() == "modified" {
		t.Error("GetAllSecrets returned mutable reference")
	}
}

func TestManager_MigratesPartialSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	// Write a partial secrets file (simulating old version)
	partialSecrets := `{
		"postgresPassword": "existing-password"
	}`
	if err := os.WriteFile(secretsPath, []byte(partialSecrets), 0600); err != nil {
		t.Fatalf("failed to write partial secrets: %v", err)
	}

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// Existing secret should be preserved
	if m.GetPostgresPassword() != "existing-password" {
		t.Errorf("expected 'existing-password', got '%s'", m.GetPostgresPassword())
	}

	// New secrets should be generated
	if m.GetAuthentikSecretKey() == "" {
		t.Error("authentikSecretKey not migrated")
	}
	if m.GetSSOHostSecret() == "" {
		t.Error("ssoHostSecret not migrated")
	}
}

func TestManager_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "secrets.json")

	m := NewManager(secretsPath)
	if err := m.Load(); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	info, err := os.Stat(secretsPath)
	if err != nil {
		t.Fatalf("failed to stat secrets file: %v", err)
	}

	// File should be owner read/write only (0600)
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %o", perm)
	}
}
