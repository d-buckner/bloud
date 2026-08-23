// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package affine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type fakeSecrets struct {
	password string
}

func (f *fakeSecrets) GenerateAppAdminPassword(_ string) (string, error) {
	return f.password, nil
}

func (f *fakeSecrets) GetAppSecret(_, _ string) string { return "" }

// configuratorForServer points a configurator at an httptest server so the
// PostStart admin-bootstrap flow can be exercised without a real server.
func configuratorForServer(t *testing.T, handler http.Handler, secrets configurator.AppSecretsProvider) *Configurator {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	var port int
	_, portStr, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	_, err = fmt.Sscanf(portStr, "%d", &port)
	require.NoError(t, err)
	return NewConfigurator(port, "http://localhost:8080", secrets, quietLogger())
}

func TestAppExternalURL_DerivesSubdomain(t *testing.T) {
	c := NewConfigurator(0, "http://localhost:8080", nil, quietLogger())
	assert.Equal(t, "http://affine.localhost:8080", c.appExternalURL())

	c = NewConfigurator(0, "http://192.168.1.5:8080", nil, quietLogger())
	assert.Equal(t, "http://affine.192.168.1.5:8080", c.appExternalURL())

	c = NewConfigurator(0, "https://bloud.example.com", nil, quietLogger())
	assert.Equal(t, "https://affine.bloud.example.com", c.appExternalURL())

	// Empty/invalid base URL falls back to the dev default.
	c = NewConfigurator(0, "", nil, quietLogger())
	assert.Equal(t, "http://affine.localhost:8080", c.appExternalURL())
	c = NewConfigurator(0, "://nonsense", nil, quietLogger())
	assert.Equal(t, "http://affine.localhost:8080", c.appExternalURL())
}

func TestRenderConfigFile_WithOIDC(t *testing.T) {
	oidc := &configurator.OIDCOutput{
		ClientID:     "affine-client",
		ClientSecret: "secret-value",
		IssuerURL:    "http://sso.localhost:8080/application/o/affine/",
	}
	content := renderConfigFile("http://affine.localhost:8080", oidc)

	var cfg struct {
		Server struct {
			ExternalURL string `json:"externalUrl"`
		} `json:"server"`
		OAuth struct {
			Providers struct {
				OIDC struct {
					ClientID          string `json:"clientId"`
					ClientSecret      string `json:"clientSecret"`
					Issuer            string `json:"issuer"`
					AllowPrivateNet   bool   `json:"allowPrivateNetwork"`
				} `json:"oidc"`
			} `json:"providers"`
		} `json:"oauth"`
	}
	require.NoError(t, json.Unmarshal([]byte(content), &cfg))
	assert.Equal(t, "http://affine.localhost:8080", cfg.Server.ExternalURL)
	assert.Equal(t, "affine-client", cfg.OAuth.Providers.OIDC.ClientID)
	assert.Equal(t, "secret-value", cfg.OAuth.Providers.OIDC.ClientSecret)
	assert.Equal(t, "http://sso.localhost:8080/application/o/affine/", cfg.OAuth.Providers.OIDC.Issuer)
	// The issuer resolves to a private address inside the VM; without this
	// flag AFFiNE's SSRF guard rejects the discovery request.
	assert.True(t, cfg.OAuth.Providers.OIDC.AllowPrivateNet)
}

func TestRenderConfigFile_WithoutOIDC(t *testing.T) {
	content := renderConfigFile("http://affine.localhost:8080", nil)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &cfg))
	server, ok := cfg["server"].(map[string]any)
	require.True(t, ok, "server section must be present")
	assert.Equal(t, "http://affine.localhost:8080", server["externalUrl"])
	_, hasOAuth := cfg["oauth"]
	assert.False(t, hasOAuth, "oauth section must be absent without SSO")
}

func TestPreStart_WritesConfigAndReportsChange(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, "http://localhost:8080", nil, quietLogger())
	state := &configurator.AppState{
		DataPath:   dataPath,
		SSOEnabled: true,
		OIDC: &configurator.OIDCOutput{
			ClientID:     "affine-client",
			ClientSecret: "secret-value",
			IssuerURL:    "http://sso.localhost:8080/application/o/affine/",
		},
	}

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed, "first write must report a config change")

	path := filepath.Join(dataPath, "config", configFileName)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), `"issuer": "http://sso.localhost:8080/application/o/affine/"`)

	// Idempotent: an identical second run must not report a change (the
	// orchestrator would otherwise recreate the container every cycle).
	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed, "identical config must not trigger a recreate")
}

func TestPreStart_WithoutOIDC_WritesServerConfigOnly(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, "http://localhost:8080", nil, quietLogger())
	state := &configurator.AppState{DataPath: dataPath}

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed)

	content, err := os.ReadFile(filepath.Join(dataPath, "config", configFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), `"externalUrl": "http://affine.localhost:8080"`)
	assert.NotContains(t, string(content), "oauth")

	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestPreStart_SecretChangeTriggersRecreate(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, "http://localhost:8080", nil, quietLogger())

	mkState := func(secret string) *configurator.AppState {
		return &configurator.AppState{
			DataPath:   dataPath,
			SSOEnabled: true,
			OIDC: &configurator.OIDCOutput{
				ClientID:     "affine-client",
				ClientSecret: secret,
				IssuerURL:    "http://sso.localhost:8080/application/o/affine/",
			},
		}
	}

	changed, err := c.PreStart(context.Background(), mkState("secret-one"))
	require.NoError(t, err)
	assert.True(t, changed)

	changed, err = c.PreStart(context.Background(), mkState("secret-one"))
	require.NoError(t, err)
	assert.False(t, changed)

	// A rotated client secret must be picked up on the next cycle.
	changed, err = c.PreStart(context.Background(), mkState("secret-two"))
	require.NoError(t, err)
	assert.True(t, changed, "rotated secret must trigger a container recreate")
}

func TestRemove_IsNoOp(t *testing.T) {
	c := NewConfigurator(0, "http://localhost:8080", nil, quietLogger())
	require.NoError(t, c.Remove(context.Background(), &configurator.AppState{}, true))
}

func TestEnsureBootstrapAdmin_CreatesOwnerOnFirstRun(t *testing.T) {
	var got map[string]string
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"u1"}`))
	})
	c := configuratorForServer(t, handler, &fakeSecrets{password: "test-password-123"})

	require.NoError(t, c.ensureBootstrapAdmin(context.Background()))
	assert.Equal(t, "/api/setup/create-admin-user", gotPath)
	assert.Equal(t, bootstrapAdminEmail, got["email"])
	assert.Equal(t, bootstrapAdminName, got["name"])
	assert.Equal(t, "test-password-123", got["password"])
}

func TestEnsureBootstrapAdmin_IdempotentWhenOwnerExists(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"First user already created"}`))
	})
	c := configuratorForServer(t, handler, &fakeSecrets{password: "test-password-123"})

	// Every reconciliation re-runs PostStart; the "already created" answer
	// must be treated as success, not an error (ERROR is terminal).
	require.NoError(t, c.ensureBootstrapAdmin(context.Background()))
}

func TestEnsureBootstrapAdmin_SurfacesOtherRejections(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Something else went wrong"}`))
	})
	c := configuratorForServer(t, handler, &fakeSecrets{password: "test-password-123"})

	err := c.ensureBootstrapAdmin(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "Something else went wrong"))
}

func TestEnsureBootstrapAdmin_RequiresSecretsProvider(t *testing.T) {
	c := NewConfigurator(0, "http://localhost:1", nil, quietLogger())
	require.Error(t, c.ensureBootstrapAdmin(context.Background()))
}
