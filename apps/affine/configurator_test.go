// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"bytes"
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

// staticBaseURL adapts a fixed URL to the configurator's func() string base
// URL interface (tests do not mutate hosts at runtime).
func staticBaseURL(u string) func() string {
	return func() string { return u }
}

type fakeSecrets struct {
	password string
}

func (f *fakeSecrets) GenerateAppAdminPassword(_ string) (string, error) {
	return f.password, nil
}

func (f *fakeSecrets) GetAppSecret(_, _ string) string { return "" }

func (f *fakeSecrets) SetAppSecret(string, string, string) error                { return nil }
func (f *fakeSecrets) SetAppContractValue(string, string, string, string) error { return nil }
func (f *fakeSecrets) GetAppContractValue(string, string, string) string        { return "" }
func (f *fakeSecrets) DeleteAppSecrets(string) error                            { return nil }

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
	c := NewConfigurator(port, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = fmt.Sprintf("http://localhost:%d", port)
	return c
}

func TestAppExternalURL_DerivesSubdomain(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://localhost:8080"), Logger: quietLogger()})
	assert.Equal(t, "http://affine.localhost:8080", c.appExternalURL())

	c = NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://192.168.1.5:8080"), Logger: quietLogger()})
	assert.Equal(t, "http://affine.192.168.1.5:8080", c.appExternalURL())

	c = NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("https://bloud.example.com"), Logger: quietLogger()})
	assert.Equal(t, "https://affine.bloud.example.com", c.appExternalURL())

	// Empty/invalid base URL falls back to the dev default.
	c = NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL(""), Logger: quietLogger()})
	assert.Equal(t, "http://affine.localhost:8080", c.appExternalURL())
	c = NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("://nonsense"), Logger: quietLogger()})
	assert.Equal(t, "http://affine.localhost:8080", c.appExternalURL())
}

func TestRenderConfigFile_WithOIDC(t *testing.T) {
	oidc := &configurator.OIDCOutput{
		ClientID:     "affine-client",
		ClientSecret: "secret-value",
		IssuerURL:    "http://sso.localhost:8080/application/o/affine/",
	}
	content, err := renderConfigFile("http://affine.localhost:8080", oidc, nil)
	require.NoError(t, err)

	var cfg struct {
		Server struct {
			ExternalURL string `json:"externalUrl"`
		} `json:"server"`
		OAuth struct {
			Providers struct {
				OIDC struct {
					ClientID        string `json:"clientId"`
					ClientSecret    string `json:"clientSecret"`
					Issuer          string `json:"issuer"`
					AllowPrivateNet bool   `json:"allowPrivateNetwork"`
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

	var whole map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &whole))
	copilot, ok := whole["copilot"].(map[string]any)
	require.True(t, ok, "copilot section must be present: MCP depends on it")
	assert.Equal(t, true, copilot["enabled"])
}

func TestRenderConfigFile_WithoutOIDC(t *testing.T) {
	content, err := renderConfigFile("http://affine.localhost:8080", nil, nil)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &cfg))
	server, ok := cfg["server"].(map[string]any)
	require.True(t, ok, "server section must be present")
	assert.Equal(t, "http://affine.localhost:8080", server["externalUrl"])
	// The MCP server lives under the copilot module and the flag defaults off,
	// so a config without it answers every MCP call "Copilot is disabled."
	copilot, ok := cfg["copilot"].(map[string]any)
	require.True(t, ok, "copilot section must be present: MCP depends on it")
	assert.Equal(t, true, copilot["enabled"])

	_, hasOAuth := cfg["oauth"]
	assert.False(t, hasOAuth, "oauth section must be absent without SSO")
}

func installedCalDAVBinding() configurator.CalDAVBinding {
	return configurator.CalDAVBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "radicale",
			Installed: true,
			Node:      "apps-radicale",
			Port:      5232,
			BaseURL:   "http://apps-radicale:5232",
		},
		Path: "/",
	}
}

func TestRenderConfigFile_WithCalDAV(t *testing.T) {
	caldav := installedCalDAVBinding()
	content, err := renderConfigFile("http://affine.localhost:8080", nil, &caldav)
	require.NoError(t, err)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &cfg))
	calendar, ok := cfg["calendar"].(map[string]any)
	require.True(t, ok, "calendar section must be present when a DAV provider is installed")
	caldavCfg, ok := calendar["caldav"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, true, caldavCfg["enabled"])
	assert.Equal(t, false, caldavCfg["allowCustomProvider"])
	assert.Equal(t, true, caldavCfg["allowInsecureHttp"])
	assert.Equal(t, false, caldavCfg["blockPrivateNetwork"])

	providers, ok := caldavCfg["providers"].([]any)
	require.True(t, ok, "providers must be an array")
	require.Len(t, providers, 1)
	provider := providers[0].(map[string]any)
	assert.Equal(t, "radicale", provider["id"])
	assert.Equal(t, "Bloud Calendar", provider["label"])
	// AFFiNE's server fetches the calendar over the container network, so the
	// address is the DAV server's node, not a browser-facing subdomain.
	assert.Equal(t, "http://apps-radicale:5232/", provider["serverUrl"])
	assert.Equal(t, "basic", provider["authType"])
}

func TestRenderConfigFile_WithoutCalDAV(t *testing.T) {
	content, err := renderConfigFile("http://affine.localhost:8080", nil, nil)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &cfg))
	_, hasCalendar := cfg["calendar"]
	assert.False(t, hasCalendar, "no DAV provider means no calendar block, so AFFiNE keeps it disabled")
}

func TestRenderCalDAVBlock_RequiresInstalledProvider(t *testing.T) {
	assert.Nil(t, renderCalDAVBlock(nil))

	notInstalled := installedCalDAVBinding()
	notInstalled.Installed = false
	assert.Nil(t, renderCalDAVBlock(&notInstalled))

	noAddress := installedCalDAVBinding()
	noAddress.BaseURL = ""
	assert.Nil(t, renderCalDAVBlock(&noAddress))
}

func TestPostStart_RestartsWhenCalDAVProviderAppears(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://localhost:8080"), Logger: quietLogger()})
	var restarted []string
	c.restartContainerFn = func(_ context.Context, name string) error {
		restarted = append(restarted, name)
		return nil
	}

	state := &configurator.AppState{DataPath: dataPath}
	// PreStart writes the config with no calendar block.
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	// A provider appears after AFFiNE is already RUNNING: the resync rewrites
	// config.json and restarts, returning before any network work.
	state.Integrations.CalDAVServers = []configurator.CalDAVBinding{installedCalDAVBinding()}
	require.NoError(t, c.PostStart(context.Background(), state))
	assert.Equal(t, []string{nodeName}, restarted)

	content, err := os.ReadFile(filepath.Join(dataPath, "config", configFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), `"serverUrl": "http://apps-radicale:5232/"`)
}

func TestPreStart_WritesConfigAndReportsChange(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://localhost:8080"), Logger: quietLogger()})
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
	assert.True(t, changed.RestartNeeded, "first write must report a config change")

	path := filepath.Join(dataPath, "config", configFileName)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), `"issuer": "http://sso.localhost:8080/application/o/affine/"`)

	// Idempotent: an identical second run must not report a change (the
	// orchestrator would otherwise recreate the container every cycle).
	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded, "identical config must not trigger a recreate")
}

func TestPreStart_WithoutOIDC_WritesServerConfigOnly(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://localhost:8080"), Logger: quietLogger()})
	state := &configurator.AppState{DataPath: dataPath}

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded)

	content, err := os.ReadFile(filepath.Join(dataPath, "config", configFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), `"externalUrl": "http://affine.localhost:8080"`)
	assert.NotContains(t, string(content), "oauth")

	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded)
}

func TestPreStart_SecretChangeTriggersRecreate(t *testing.T) {
	dataPath := t.TempDir()
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://localhost:8080"), Logger: quietLogger()})

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
	assert.True(t, changed.RestartNeeded)

	changed, err = c.PreStart(context.Background(), mkState("secret-one"))
	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded)

	// A rotated client secret must be picked up on the next cycle.
	changed, err = c.PreStart(context.Background(), mkState("secret-two"))
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "rotated secret must trigger a container recreate")
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
	assert.Equal(t, fallbackAdminEmail, got["email"], "no operator email supplied, so the local fallback is used")
	assert.Equal(t, bootstrapAdminName, got["name"])
	assert.Equal(t, "test-password-123", got["password"])
}

// The first user is created with the operator's SSO identity email, not a
// synthetic address, so AFFiNE links the operator's OIDC login to this account
// (AFFiNE links by email) and the operator owns the wired workspace.
func TestEnsureBootstrapAdmin_UsesOperatorEmail(t *testing.T) {
	var got map[string]string
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"u1"}`))
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c := NewConfigurator(0, configurator.Deps{
		Secrets:       &fakeSecrets{password: "test-password-123"},
		OperatorEmail: "admin@localhost.local",
		Logger:        quietLogger(),
	})
	c.baseURL = server.URL

	require.NoError(t, c.ensureBootstrapAdmin(context.Background()))
	assert.Equal(t, "/api/setup/create-admin-user", gotPath)
	assert.Equal(t, "admin@localhost.local", got["email"], "the operator's identity, not a synthetic address")
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
	c := NewConfigurator(0, configurator.Deps{PrimaryBaseURL: staticBaseURL("http://localhost:1"), Logger: quietLogger()})
	require.Error(t, c.ensureBootstrapAdmin(context.Background()))
}

// TestWorkspaceInitDocCarriesTheSharedName guards the hand-written Yjs bytes
// against drifting from the name Bloud says it creates. No API call sets the
// workspace name on self-host, so these bytes are the only place the name is
// decided and this is the only check that they still agree with the constant.
func TestWorkspaceInitDocCarriesTheSharedName(t *testing.T) {
	assert.True(t, bytes.Contains(workspaceInitDoc, []byte(sharedWorkspaceName)),
		"the seeded workspace document must carry the name Bloud advertises")
	assert.False(t, bytes.Contains(workspaceInitDoc, []byte("Bloud")),
		"the workspace is named for what it is, not for who made it")
}
