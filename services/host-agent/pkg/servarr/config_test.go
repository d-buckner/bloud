// SPDX-License-Identifier: AGPL-3.0-only

package servarr

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/xmlutil"
)

// apiKeyPattern is the shape Servarr itself generates: 32 lowercase hex chars.
var apiKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// existingAPIKey is a key an instance generated itself; every config-writing
// test asserts it survives untouched.
const existingAPIKey = "0123456789abcdef0123456789abcdef"

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// configPath returns a path inside a fresh temp dir, in the same
// <dataDir>/config/config.xml layout the containers mount.
func configPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config", "config.xml")
}

func openConfig(t *testing.T, path string) *xmlutil.ConfigFile {
	t.Helper()
	cfg, err := xmlutil.Open(path, rootElement)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return cfg
}

func TestEnsureExternalAuth_CreatesConfigWithGeneratedKey(t *testing.T) {
	path := configPath(t)

	changed, err := EnsureExternalAuth(path)
	if err != nil {
		t.Fatalf("EnsureExternalAuth() error = %v", err)
	}
	if !changed {
		t.Error("EnsureExternalAuth() changed = false, want true for a missing config.xml")
	}

	cfg := openConfig(t, path)
	if got := cfg.GetElement(authMethodElement); got != externalAuthMode {
		t.Errorf("%s = %q, want %q", authMethodElement, got, externalAuthMode)
	}
	if got := cfg.GetElement(authRequiredElement); got != authRequiredEnabled {
		t.Errorf("%s = %q, want %q", authRequiredElement, got, authRequiredEnabled)
	}
	key := cfg.GetElement(apiKeyElement)
	if !apiKeyPattern.MatchString(key) {
		t.Errorf("%s = %q, want 32 lowercase hex characters", apiKeyElement, key)
	}

	// The key PreStart wrote must be the key PostStart reads back.
	read, err := APIKey(path)
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if read != key {
		t.Errorf("APIKey() = %q, want the generated %q", read, key)
	}
}

func TestEnsureExternalAuth_Idempotent(t *testing.T) {
	path := configPath(t)

	if _, err := EnsureExternalAuth(path); err != nil {
		t.Fatalf("first EnsureExternalAuth() error = %v", err)
	}
	afterFirst := readFile(t, path)

	changed, err := EnsureExternalAuth(path)
	if err != nil {
		t.Fatalf("second EnsureExternalAuth() error = %v", err)
	}
	if changed {
		t.Error("second EnsureExternalAuth() changed = true, want false (would restart the container every cycle)")
	}
	if got := readFile(t, path); got != afterFirst {
		t.Errorf("second run rewrote the file:\n got %q\nwant %q", got, afterFirst)
	}
}

func TestEnsureExternalAuth_PreservesUnrelatedKeysAndExistingAPIKey(t *testing.T) {
	path := configPath(t)
	writeFile(t, path, `<?xml version="1.0" encoding="utf-8"?>
<Config>
  <Port>8989</Port>
  <UrlBase></UrlBase>
  <Branch>main</Branch>
  <ApiKey>`+existingAPIKey+`</ApiKey>
</Config>`)

	changed, err := EnsureExternalAuth(path)
	if err != nil {
		t.Fatalf("EnsureExternalAuth() error = %v", err)
	}
	if !changed {
		t.Error("EnsureExternalAuth() changed = false, want true: the auth keys were missing")
	}

	cfg := openConfig(t, path)
	if got := cfg.GetElement(apiKeyElement); got != existingAPIKey {
		t.Errorf("%s = %q, want the instance's own key %q", apiKeyElement, got, existingAPIKey)
	}
	if got := cfg.GetElement("Port"); got != "8989" {
		t.Errorf("Port = %q, want %q (unrelated keys must survive)", got, "8989")
	}
	if got := cfg.GetElement("Branch"); got != "main" {
		t.Errorf("Branch = %q, want %q (unrelated keys must survive)", got, "main")
	}
	if got := cfg.GetElement(authMethodElement); got != externalAuthMode {
		t.Errorf("%s = %q, want %q", authMethodElement, got, externalAuthMode)
	}
	if got := cfg.GetElement(authRequiredElement); got != authRequiredEnabled {
		t.Errorf("%s = %q, want %q", authRequiredElement, got, authRequiredEnabled)
	}
}

// TestEnsureExternalAuth_TolerantOfAppReserialisation pins the flap guard: the
// app rewrites config.xml with its own serializer whenever its settings are
// saved, so a semantically-correct file in a different layout must not be
// reported as a change (PreStart's changed flag recreates the container).
func TestEnsureExternalAuth_TolerantOfAppReserialisation(t *testing.T) {
	path := configPath(t)
	seed := "<Config>\n    <AuthenticationMethod>External</AuthenticationMethod>\n" +
		"    <AuthenticationRequired>Enabled</AuthenticationRequired>\n" +
		"    <ApiKey>" + existingAPIKey + "</ApiKey>\n</Config>"
	writeFile(t, path, seed)

	changed, err := EnsureExternalAuth(path)
	if err != nil {
		t.Fatalf("EnsureExternalAuth() error = %v", err)
	}
	if changed {
		t.Error("EnsureExternalAuth() changed = true for an already-correct file in the app's own layout")
	}
	if got := readFile(t, path); got != seed {
		t.Errorf("file was rewritten:\n got %q\nwant %q", got, seed)
	}
}

func TestEnsureExternalAuth_MalformedFileErrors(t *testing.T) {
	path := configPath(t)
	writeFile(t, path, "<Config><ApiKey>")

	if _, err := EnsureExternalAuth(path); err == nil {
		t.Error("EnsureExternalAuth() error = nil, want an error for malformed XML")
	}
	if _, err := APIKey(path); err == nil {
		t.Error("APIKey() error = nil, want an error for malformed XML")
	}
}

func TestAPIKey_MissingFileIsEmptyAndReadOnly(t *testing.T) {
	path := configPath(t)

	key, err := APIKey(path)
	if err != nil {
		t.Fatalf("APIKey() error = %v", err)
	}
	if key != "" {
		t.Errorf("APIKey() = %q, want \"\"", key)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("APIKey() created %s; reading config must be side-effect free", path)
	}
}
