// SPDX-License-Identifier: AGPL-3.0-only

package immich

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeAppSecrets is an in-memory AppSecretsProvider for the appToken publishing
// path: it records what the configurator stores and hands it back on the next
// pass, which is the cycle that decides whether a key is reused or replaced.
type fakeAppSecrets struct {
	secrets map[string]string
}

func newFakeAppSecrets() *fakeAppSecrets { return &fakeAppSecrets{secrets: map[string]string{}} }

func (s *fakeAppSecrets) GenerateAppAdminPassword(string) (string, error) {
	return "admin-password", nil
}

func (s *fakeAppSecrets) GetAppSecret(_, key string) string { return s.secrets[key] }

func (s *fakeAppSecrets) SetAppSecret(_, key, value string) error {
	s.secrets[key] = value
	return nil
}

func (s *fakeAppSecrets) SetAppContractValue(string, string, string, string) error { return nil }

func (s *fakeAppSecrets) GetAppContractValue(string, string, string) string { return "" }

// companionAPI points a configurator at an httptest server through the baseURL
// seam, so the publishing path is exercised through the real appclient.
func companionAPI(t *testing.T, handler http.HandlerFunc, secrets *fakeAppSecrets) *Configurator {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = srv.URL
	return c
}

// TestEnsureCompanionToken_MintsAndPublishes covers the first pass: nothing is
// stored, so the configurator mints a key and publishes it.
func TestEnsureCompanionToken_MintsAndPublishes(t *testing.T) {
	secrets := newFakeAppSecrets()
	minted := false
	c := companionAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/api-keys":
			minted = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"k","name":"bloud-immich-mcp","secret":"minted-key"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}, secrets)

	c.ensureCompanionToken(context.Background(), "admin-session")
	require.True(t, minted)
	assert.Equal(t, "minted-key", secrets.GetAppSecret(appName, "token"))
}

// TestEnsureCompanionToken_ReusesValidKey is the steady state: the stored key
// still authenticates, so the next pass republishes nothing.
func TestEnsureCompanionToken_ReusesValidKey(t *testing.T) {
	secrets := newFakeAppSecrets()
	secrets.secrets["token"] = "good-key"
	c := companionAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/api-keys/me", r.URL.Path)
		require.Equal(t, "good-key", r.Header.Get("x-api-key"))
		w.WriteHeader(http.StatusOK)
	}, secrets)

	c.ensureCompanionToken(context.Background(), "admin-session")
	assert.Equal(t, "good-key", secrets.GetAppSecret(appName, "token"))
}

// TestEnsureCompanionToken_KeepsKeyOnTransientFault makes sure a 5xx does not
// rotate a working credential: only a rejection means the key is gone.
func TestEnsureCompanionToken_KeepsKeyOnTransientFault(t *testing.T) {
	secrets := newFakeAppSecrets()
	secrets.secrets["token"] = "good-key"
	c := companionAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, secrets)

	c.ensureCompanionToken(context.Background(), "admin-session")
	assert.Equal(t, "good-key", secrets.GetAppSecret(appName, "token"))
}

// TestEnsureCompanionToken_ReplacesRejectedKey covers revocation: Immich
// rejects the stored key, so the configurator clears the old row and mints a
// fresh one.
func TestEnsureCompanionToken_ReplacesRejectedKey(t *testing.T) {
	secrets := newFakeAppSecrets()
	secrets.secrets["token"] = "stale-key"
	deleted := false
	c := companionAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/api-keys/me":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodGet && r.URL.Path == "/api/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"old","name":"bloud-immich-mcp"}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/api-keys/old":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/api-keys":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"new","name":"bloud-immich-mcp","secret":"fresh-key"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}, secrets)

	c.ensureCompanionToken(context.Background(), "admin-session")
	require.True(t, deleted, "the stale key's row is cleared before minting")
	assert.Equal(t, "fresh-key", secrets.GetAppSecret(appName, "token"))
}

func TestEnsureMountMarkers_CreatesAllFoldersAndMarkers(t *testing.T) {
	dataPath := t.TempDir()

	require.NoError(t, ensureMountMarkers(dataPath, quietLogger()))

	for _, folder := range mountFolders {
		marker := filepath.Join(dataPath, "upload", folder, ".immich")
		info, err := os.Stat(marker)
		require.NoError(t, err, "marker for %s must exist", folder)
		assert.False(t, info.IsDir(), "marker for %s must be a file", folder)
		content, err := os.ReadFile(marker)
		require.NoError(t, err)
		assert.NotEmpty(t, content, "marker for %s must carry content", folder)
	}
}

func TestEnsureMountMarkers_IsIdempotent(t *testing.T) {
	dataPath := t.TempDir()
	logger := quietLogger()

	require.NoError(t, ensureMountMarkers(dataPath, logger))

	// First markers' content must survive a second run (the server may have
	// recorded the check as passed; only existence matters to it).
	first := map[string][]byte{}
	for _, folder := range mountFolders {
		content, err := os.ReadFile(filepath.Join(dataPath, "upload", folder, ".immich"))
		require.NoError(t, err)
		first[folder] = content
	}

	require.NoError(t, ensureMountMarkers(dataPath, logger))

	for folder, before := range first {
		after, err := os.ReadFile(filepath.Join(dataPath, "upload", folder, ".immich"))
		require.NoError(t, err)
		assert.Equal(t, before, after, "marker for %s must not be rewritten", folder)
	}
}
