// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// retiredFallbacks are the static credentials that used to be reachable from
// config.Load's fallback path. They must never appear as a resolved value again:
// their presence in the resolution table would be a downgrade-to-known-creds bug.
var retiredFallbacks = []string{
	"testpass123",
	"dev-secret-change-in-production",
	"password",
	"ldap-bind-password-change-in-production",
	"test-bootstrap-token-change-in-production",
}

// discardLogger keeps test output quiet without changing resolution behavior.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// loadInDir runs Load with BLOUD_DATA_DIR pointed at dir and the four required
// secret env vars cleared, so resolution is driven purely by the on-disk store.
func loadInDir(t *testing.T, dir string) (*Config, error) {
	t.Helper()
	t.Setenv("BLOUD_DATA_DIR", dir)
	t.Setenv("BLOUD_POSTGRES_PASSWORD", "")
	t.Setenv("BLOUD_SSO_HOST_SECRET", "")
	t.Setenv("BLOUD_AUTHENTIK_ADMIN_PASSWORD", "")
	t.Setenv("BLOUD_LDAP_BIND_PASSWORD", "")
	return LoadWithLogger(discardLogger())
}

// TestLoad_MissingSecretsAutoGenerates proves the happy path: with no secrets.json
// and no env overrides, Load boots by generating a fresh store, the file is
// created on disk, and every required secret is a non-empty generated value that
// is none of the retired fallback strings.
func TestLoad_MissingSecretsAutoGenerates(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadInDir(t, dir)
	if err != nil {
		t.Fatalf("Load() with writable empty dir should succeed, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "secrets.json")); err != nil {
		t.Fatalf("expected secrets.json to be created: %v", err)
	}

	resolved := map[string]string{
		"postgres":  cfg.PostgresPassword,
		"ssoHost":   cfg.SSOHostSecret,
		"authentik": cfg.AuthentikAdminPassword,
		"ldapBind":  cfg.LDAPBindPassword,
	}
	for name, val := range resolved {
		if val == "" {
			t.Errorf("%s resolved empty", name)
		}
		for _, bad := range retiredFallbacks {
			if val == bad {
				t.Errorf("%s resolved to retired fallback %q", name, bad)
			}
		}
	}
}

// TestLoad_CorruptSecretsFailsAndPreservesFile is the security property: a
// corrupt secrets.json must be fatal, not silently regenerated over; the
// original bytes are left intact so the operator can recover them.
func TestLoad_CorruptSecretsFailsAndPreservesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	garbage := []byte("{ this is not valid json }")
	if err := os.WriteFile(path, garbage, 0600); err != nil {
		t.Fatalf("seeding corrupt secrets: %v", err)
	}

	_, err := loadInDir(t, dir)
	if err == nil {
		t.Fatal("Load() with corrupt secrets.json must error, got nil")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("re-reading secrets.json: %v", readErr)
	}
	if string(got) != string(garbage) {
		t.Errorf("corrupt secrets.json was overwritten; original bytes must be preserved\n got: %q\nwant: %q", got, garbage)
	}
}

// TestLoad_EnvOverrideWins proves precedence: an explicit env secret is used even
// when the store auto-generates a different value.
func TestLoad_EnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLOUD_DATA_DIR", dir)
	t.Setenv("BLOUD_POSTGRES_PASSWORD", "from-env-override")
	t.Setenv("BLOUD_SSO_HOST_SECRET", "")
	t.Setenv("BLOUD_AUTHENTIK_ADMIN_PASSWORD", "")
	t.Setenv("BLOUD_LDAP_BIND_PASSWORD", "")

	cfg, err := LoadWithLogger(discardLogger())
	if err != nil {
		t.Fatalf("Load() should succeed with env override, got: %v", err)
	}
	if cfg.PostgresPassword != "from-env-override" {
		t.Errorf("env var must win: got %q, want %q", cfg.PostgresPassword, "from-env-override")
	}
}

// TestGetSecret_ErrorsWhenUnresolvable proves the getSecret primitive returns an
// error (not a fallback) when both env and store are empty.
func TestGetSecret_ErrorsWhenUnresolvable(t *testing.T) {
	t.Setenv("BLOUD_TEST_SECRET", "")
	if _, err := getSecret("BLOUD_TEST_SECRET", ""); err == nil {
		t.Fatal("getSecret with empty env and empty store must error")
	}
}
