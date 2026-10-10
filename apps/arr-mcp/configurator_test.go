// SPDX-License-Identifier: AGPL-3.0-only

package arrmcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/require"
)

// fakeSecrets implements configurator.AppSecretsProvider with an in-memory
// map, so PreStart's mint-and-persist flow can be observed.
type fakeSecrets struct {
	values map[string]string
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{values: map[string]string{}}
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (f *fakeSecrets) GetAppSecret(_, key string) string               { return f.values[key] }
func (f *fakeSecrets) SetAppSecret(_, key, value string) error {
	f.values[key] = value
	return nil
}
func (f *fakeSecrets) SetAppContractValue(_, _, _, _ string) error { return nil }
func (f *fakeSecrets) GetAppContractValue(_, _, _ string) string   { return "" }
func (f *fakeSecrets) DeleteAppSecrets(string) error               { return nil }

func writeConfig(t *testing.T, c *Configurator, state *configurator.AppState) string {
	t.Helper()
	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(state.DataPath, configDir, configFileName))
	require.NoError(t, err)
	return string(content)
}

// TestPreStartUnboundIsOfflineAndIdempotent pins the unbound shape the
// conformance harness also exercises: no service bindings means the config is
// auth-only, written with no network, and a second pass reports no change.
func TestPreStartUnboundIsOfflineAndIdempotent(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})
	state := &configurator.AppState{DataPath: t.TempDir(), Integrations: configurator.Integrations{}}

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded, "first write must recreate the container")

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded, "steady-state PreStart must not ask for a recreate")

	content, err := os.ReadFile(filepath.Join(state.DataPath, configDir, configFileName))
	require.NoError(t, err)
	s := string(content)
	require.Contains(t, s, "username: bloud")
	require.Contains(t, s, "tier: read")
	require.Contains(t, s, "sha256:")
	require.Contains(t, s, "scrypt$")
	require.NotContains(t, s, "seerr:", "an unbound config writes no service blocks")

	require.NotEmpty(t, secrets.values[httpTokenKey], "the inbound bearer must be published")
	require.NotEmpty(t, secrets.values[clientPasswordKey], "the UI password must be published")
}

// TestPreStartRendersSeerr pins the one binding this app is built around: the
// request manager's address, key and default user land in the config verbatim.
func TestPreStartRendersSeerr(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{
		DataPath: t.TempDir(),
		Integrations: configurator.Integrations{
			RequestManagers: []configurator.RequestManagerBinding{{
				ProviderRef: configurator.ProviderRef{
					App: "seerr", Node: "apps-seerr", Port: 5055,
					BaseURL: "http://apps-seerr:5055", LocalURL: "http://localhost:5055",
					Installed: true,
				},
				APIKey:      "seerr-key",
				DefaultUser: "bloud-bootstrap-admin",
			}},
		},
	}

	s := writeConfig(t, c, state)
	require.Contains(t, s, "seerr:")
	require.Contains(t, s, "url: http://apps-seerr:5055")
	require.Contains(t, s, "api_key: seerr-key")
	require.Contains(t, s, "default_user: bloud-bootstrap-admin")
}

// TestPreStartRendersPVRs pins the optional multi contract: whichever Servarrs
// are installed land under their own service key.
func TestPreStartRendersPVRs(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: newFakeSecrets()})
	state := &configurator.AppState{
		DataPath: t.TempDir(),
		Integrations: configurator.Integrations{
			PVRs: []configurator.PVRBinding{
				{ProviderRef: configurator.ProviderRef{App: "radarr", Node: "apps-radarr", Port: 7878, BaseURL: "http://apps-radarr:7878", Installed: true}, APIKey: "radarr-key"},
				{ProviderRef: configurator.ProviderRef{App: "sonarr", Node: "apps-sonarr", Port: 8989, BaseURL: "http://apps-sonarr:8989", Installed: true}, APIKey: "sonarr-key"},
			},
		},
	}

	s := writeConfig(t, c, state)
	require.Contains(t, s, "radarr:")
	require.Contains(t, s, "api_key: radarr-key")
	require.Contains(t, s, "sonarr:")
	require.Contains(t, s, "api_key: sonarr-key")
}

// TestPreStartSetsJellyfinDefaultUser pins that the media server's bootstrap
// admin becomes arr-mcp's jellyfin default_user, so the agent has an account to
// act as without the operator naming one by hand. The jellyfin key is
// pre-seeded, so the mint is skipped and PreStart stays offline.
func TestPreStartSetsJellyfinDefaultUser(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.values[jellyfinAPIKeyKey] = "cached-key"
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets})
	state := &configurator.AppState{
		DataPath: t.TempDir(),
		Integrations: configurator.Integrations{
			MediaServers: []configurator.MediaServerBinding{{
				ProviderRef: configurator.ProviderRef{
					App: "jellyfin", Node: "apps-jellyfin", Port: 8096,
					BaseURL: "http://apps-jellyfin:8096", LocalURL: "http://localhost:8096",
					Installed: true,
				},
				AdminUsername: "bloud-bootstrap-admin",
				AdminPassword: "pw",
			}},
		},
	}

	s := writeConfig(t, c, state)
	require.Contains(t, s, "jellyfin:")
	require.Contains(t, s, "url: http://apps-jellyfin:8096")
	require.Contains(t, s, "api_key: cached-key")
	require.Contains(t, s, "default_user: bloud-bootstrap-admin")
}

// TestScryptHashIsDeterministic pins the reason the salt is derived rather than
// random: a steady-state resync must write the same hash, or every pass would
// report a change and restart the container forever.
func TestScryptHashIsDeterministic(t *testing.T) {
	a, err := scryptHash("password")
	require.NoError(t, err)
	b, err := scryptHash("password")
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.True(t, strings.HasPrefix(a, "scrypt$"), "hash must be scrypt$<salt>$<hash>")
}

func TestSha256TokenHash(t *testing.T) {
	h := sha256TokenHash("token")
	require.True(t, strings.HasPrefix(h, "sha256:"), "hash must be sha256:<hex>")
	require.Len(t, strings.TrimPrefix(h, "sha256:"), 64)
	require.NotEqual(t, sha256TokenHash("a"), sha256TokenHash("b"))
}

// sseMessage writes one server-sent-events message, the framing arr-mcp uses
// for every JSON-RPC answer.
func sseMessage(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("event: message\ndata: " + payload + "\n\n"))
}

// TestPostStartToleratesStatelessServer pins the behaviour the first live
// install caught: arr-mcp answers `initialize` with no Mcp-Session-Id header.
// A probe that required a session id fails against the real server, so this
// fake refuses to issue one and PostStart must still converge.
func TestPostStartToleratesStatelessServer(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	state := &configurator.AppState{
		DataPath: t.TempDir(),
		Integrations: configurator.Integrations{
			RequestManagers: []configurator.RequestManagerBinding{{
				ProviderRef: configurator.ProviderRef{
					App: "seerr", Node: "apps-seerr", Port: 5055,
					BaseURL: "http://apps-seerr:5055", LocalURL: "http://localhost:5055",
					Installed: true,
				},
				APIKey:      "seerr-key",
				DefaultUser: "bloud-bootstrap-admin",
			}},
		},
	}
	_, err := c.PreStart(t.Context(), state)
	require.NoError(t, err)
	bearer := c.currentBearer()
	require.NotEmpty(t, bearer)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mcpEndpoint {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		switch {
		case strings.Contains(body, `"initialize"`):
			// No session header on purpose: stateless, the mode arr-mcp runs.
			sseMessage(w, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"arr-mcp","version":"1.41.1"}}}`)
		case strings.Contains(body, `"notifications/initialized"`):
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(body, `"tools/call"`):
			sseMessage(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"All 1 configured service(s) healthy."}],"structuredContent":{"services":[{"ok":true,"service":"seerr"}],"degraded":[]}}}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	c.baseURL = srv.URL

	require.NoError(t, c.PostStart(t.Context(), state))
}

// TestPostStartPassesWithNothingWired pins that an arr-mcp installed without
// Seerr (or any provider) still converges: the handshake proves the server is
// serving, and stack_health answering with an empty services list is a valid
// empty state, not a fault.
func TestPostStartPassesWithNothingWired(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, HTTP: configurator.ClientFactory{}})
	state := &configurator.AppState{DataPath: t.TempDir()}
	_, err := c.PreStart(t.Context(), state)
	require.NoError(t, err)
	bearer := c.currentBearer()
	require.NotEmpty(t, bearer)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mcpEndpoint {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		switch {
		case strings.Contains(body, `"initialize"`):
			sseMessage(w, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"arr-mcp","version":"1.41.1"}}}`)
		case strings.Contains(body, `"notifications/initialized"`):
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(body, `"tools/call"`):
			sseMessage(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"No configured services."}],"structuredContent":{"services":[],"degraded":[]}}}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	c.baseURL = srv.URL

	require.NoError(t, c.PostStart(t.Context(), state))
}
