// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// storeSecrets is an in-memory AppSecretsProvider that keeps what the
// configurator publishes, so the mint-once contract can be exercised across
// passes.
type storeSecrets struct {
	mu      sync.Mutex
	secrets map[string]string
}

func newStoreSecrets() *storeSecrets {
	return &storeSecrets{secrets: map[string]string{}}
}

func (s *storeSecrets) GenerateAppAdminPassword(string) (string, error) { return "admin", nil }

func (s *storeSecrets) GetAppSecret(_, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[key]
}

func (s *storeSecrets) SetAppSecret(_, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[key] = value
	return nil
}

func (s *storeSecrets) SetAppContractValue(_, contract, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[contract+"/"+key] = value
	return nil
}

func (s *storeSecrets) GetAppContractValue(_, contract, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[contract+"/"+key]
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testDeps builds a configurator with an in-memory secret store, a static
// base URL, and a recording exec channel. Passing nil for secrets produces a
// genuinely nil AppSecretsProvider, which is the CLI/conformance shape.
func testDeps(secrets configurator.AppSecretsProvider, exec configurator.ExecFunc) configurator.Deps {
	return configurator.Deps{
		Logger:         quietLogger(),
		Secrets:        secrets,
		PrimaryBaseURL: func() string { return "https://bloud.example.com" },
		Exec:           exec,
	}
}

// testState builds the AppState the configurator needs: a data dir, an OIDC
// output, and a wired agentApi provider.
func testState(t *testing.T) *configurator.AppState {
	t.Helper()
	dir := t.TempDir()
	return &configurator.AppState{
		DataPath:      dir,
		BloudDataPath: dir,
		SSOEnabled:    true,
		OIDC: &configurator.OIDCOutput{
			ClientID:     "webui-client",
			ClientSecret: "webui-secret",
			IssuerURL:    "https://sso.example.com/application/o/webui/",
			RedirectURI:  "https://hermes.example.com/api/auth/oidc/callback",
		},
		Integrations: configurator.Integrations{
			AgentAPIs: []configurator.AgentAPIBinding{{
				Endpoint:  "https://hermes.example.com/v1",
				APIKey:    "agent-key",
				ModelName: "hermes",
			}},
		},
	}
}

// TestPreStartMintsAndDeliversTheClientPassword pins the D work item: the
// credential is generated on first pass, persisted under the published secret
// key, and written into the sourced dotenv with the rest of the app config.
func TestPreStartMintsAndDeliversTheClientPassword(t *testing.T) {
	secrets := newStoreSecrets()
	cfg := NewConfigurator(0, testDeps(secrets, nil))
	state := testState(t)

	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)

	minted := secrets.GetAppSecret(appName, clientPasswordSecret)
	require.NotEmpty(t, minted)

	// The dotenv carries the value, the gateway wiring, and the OIDC client.
	content, err := os.ReadFile(filepath.Join(state.DataPath, "config", envFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), "HERMES_WEBUI_PASSWORD='"+minted+"'")
	assert.Contains(t, string(content), "HERMES_WEBUI_CHAT_BACKEND='gateway'")
	assert.Contains(t, string(content), "HERMES_WEBUI_GATEWAY_BASE_URL='https://hermes.example.com/v1'")
	assert.Contains(t, string(content), "HERMES_WEBUI_OIDC_CLIENT_ID='webui-client'")
	assert.Contains(t, string(content), "HERMES_WEBUI_SECURE='true'")
}

// TestPreStartIsIdempotentAcrossResync pins the invariant-2 contract: a second
// pass with the same inputs mints nothing new, rewrites nothing, and asks for
// no recreate.
func TestPreStartIsIdempotentAcrossResync(t *testing.T) {
	secrets := newStoreSecrets()
	cfg := NewConfigurator(0, testDeps(secrets, nil))
	state := testState(t)

	first, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded, "the first write must ask for the recreate")

	firstMinted := secrets.GetAppSecret(appName, clientPasswordSecret)

	second, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a steady-state resync must not ask for a recreate")
	assert.Equal(t, firstMinted, secrets.GetAppSecret(appName, clientPasswordSecret),
		"the password is minted once, not per pass")
}

// TestPreStartWithoutSecretsStillWritesConfig pins the nil-store degradation
// the conformance harness relies on: with no store, the app config is written
// on its OIDC path alone rather than failing the pass.
func TestPreStartWithoutSecretsStillWritesConfig(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := testState(t)

	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(state.DataPath, "config", envFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "HERMES_WEBUI_PASSWORD=",
		"no password is written when there is no store to hold it")
	assert.Contains(t, string(content), "HERMES_WEBUI_OIDC_CLIENT_ID='webui-client'")
}

// TestRevokeSessionsClearsTheStoreThroughExec pins G: revocation runs the
// removal inside the container, not against the host path.
func TestRevokeSessionsClearsTheStoreThroughExec(t *testing.T) {
	var execCalls []string
	exec := func(_ context.Context, container string, _ map[string]string, cmd []string) ([]byte, error) {
		execCalls = append(execCalls, container+":"+strings.Join(cmd, " "))
		return nil, nil
	}

	cfg := NewConfigurator(0, testDeps(nil, exec))
	err := cfg.RevokeSessions(context.Background(), testState(t))
	require.NoError(t, err)

	require.Len(t, execCalls, 1)
	assert.Equal(t, "apps-hermes-webui:rm -f /data/.sessions.json", execCalls[0])
}

// TestRevokeSessionsRequiresAnExecChannel pins the failure mode: without the
// host runtime callback there is no way to reach the container, and pretending
// the sessions were cleared would be a lie.
func TestRevokeSessionsRequiresAnExecChannel(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	err := cfg.RevokeSessions(context.Background(), testState(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no exec channel")
}

// TestRenderEnvOmitsEmptyValues pins writeEnv's "unset, not empty" rule: the
// app treats an absent variable as unconfigured and an empty one as configured
// with nothing, which are different states.
func TestRenderEnvOmitsEmptyValues(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: t.TempDir()}

	out := cfg.renderEnv(agentConfig{}, state, "")
	assert.NotContains(t, out, "HERMES_WEBUI_PASSWORD")
	assert.NotContains(t, out, "HERMES_WEBUI_GATEWAY_BASE_URL")
	assert.NotContains(t, out, "HERMES_WEBUI_OIDC_CLIENT_ID")
}
