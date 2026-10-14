// SPDX-License-Identifier: AGPL-3.0-only

package podly

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

// storeSecrets is an in-memory AppSecretsProvider. rotate stands for the
// Bloud-side rotation a user triggers: it replaces the client password the way
// the credential module does, by writing a new value under the published key.
type storeSecrets struct {
	mu      sync.Mutex
	secrets map[string]string
	admin   string
}

func newStoreSecrets() *storeSecrets {
	return &storeSecrets{
		secrets: map[string]string{},
		admin:   "service-password",
	}
}

func (s *storeSecrets) GenerateAppAdminPassword(string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admin, nil
}

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

func (s *storeSecrets) DeleteAppSecrets(string) error { return nil }

// rotate replaces the client password and returns the new value.
func (s *storeSecrets) rotate(value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[clientPasswordSecret] = value
	return value
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testState(t *testing.T) *configurator.AppState {
	t.Helper()
	dir := t.TempDir()
	return &configurator.AppState{DataPath: dir, BloudDataPath: dir}
}

// newConfigurator builds a configurator against an in-memory secret store. A
// nil secrets produces the genuinely-nil provider the CLI and the conformance
// harness hand a configurator.
func newConfigurator(secrets configurator.AppSecretsProvider) *Configurator {
	return &Configurator{
		port:    defaultPort,
		secrets: secrets,
		logger:  quietLogger(),
	}
}

func envPath(state *configurator.AppState) string {
	return filepath.Join(state.DataPath, configDir, envFileName)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return string(raw)
}

// TestPreStartWritesTheEnvFileTheContainerBootsFrom pins the four values the
// app needs and the recreate that makes them take effect.
func TestPreStartWritesTheEnvFileTheContainerBootsFrom(t *testing.T) {
	secrets := newStoreSecrets()
	cfg := newConfigurator(secrets)
	state := testState(t)

	result, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, result.RestartNeeded, "a first write must recreate: the values are read at container create")
	require.NotEmpty(t, result.Reason)

	values := readEnvFile(envPath(state))
	assert.Equal(t, "true", values["REQUIRE_AUTH"])
	assert.Equal(t, serviceUser, values["PODLY_ADMIN_USERNAME"])
	assert.Equal(t, "service-password", values["PODLY_ADMIN_PASSWORD"])
	assert.NotEmpty(t, values["PODLY_SECRET_KEY"], "without a stable key every restart signs out every session")

	info, err := os.Stat(envPath(state))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file holds a password")
}

// TestPreStartIsAReadOnlyDiffOnceStable is the invariant-2 contract: the
// periodic resync runs PreStart on every pass, so a pass with nothing to change
// must change nothing and must not ask for a recreate.
func TestPreStartIsAReadOnlyDiffOnceStable(t *testing.T) {
	cfg := newConfigurator(newStoreSecrets())
	state := testState(t)

	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	first := readFile(t, envPath(state))
	key := readEnvFile(envPath(state))["PODLY_SECRET_KEY"]

	for i := 0; i < 3; i++ {
		result, err := cfg.PreStart(context.Background(), state)
		require.NoError(t, err)
		assert.False(t, result.RestartNeeded, "pass %d asked for a recreate with nothing to apply", i)
	}

	assert.Equal(t, first, readFile(t, envPath(state)))
	assert.Equal(t, key, readEnvFile(envPath(state))["PODLY_SECRET_KEY"],
		"a resync that rotated the signing key would log every user out every minute")
}

// TestPreStartWithoutASecretStoreStaysDeterministic covers the CLI and
// conformance shape. The file is still written, and written the same way twice:
// minting a session key here would make every pass rewrite the file and force a
// recreate in a context that has no container to recreate.
func TestPreStartWithoutASecretStoreStaysDeterministic(t *testing.T) {
	cfg := newConfigurator(nil)
	state := testState(t)

	first, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)

	second, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded)

	values := readEnvFile(envPath(state))
	_, hasPassword := values["PODLY_ADMIN_PASSWORD"]
	assert.False(t, hasPassword, "there is no password to write; an empty one fails the app's own bootstrap anyway")
	assert.Equal(t, "true", values["REQUIRE_AUTH"], "auth is never written off")
	assert.NotEmpty(t, values["PODLY_SECRET_KEY"])
}

// TestRevokeSessionsRotatesOnlyTheSigningKey pins what the revoke control
// actually does. The app has no session store to clear, so ending sessions means
// changing the key their signatures are checked against, and nothing else in
// the file may move: the service password in particular has to stay valid, or
// the next pass could not sign in to reconcile accounts.
func TestRevokeSessionsRotatesOnlyTheSigningKey(t *testing.T) {
	secrets := newStoreSecrets()
	cfg := newConfigurator(secrets)
	state := testState(t)

	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	before := readEnvFile(envPath(state))

	require.NoError(t, cfg.RevokeSessions(context.Background(), state))

	after := readEnvFile(envPath(state))
	assert.NotEqual(t, before["PODLY_SECRET_KEY"], after["PODLY_SECRET_KEY"],
		"the sessions signed under the old key are still valid if the key did not change")
	assert.Equal(t, before["PODLY_ADMIN_PASSWORD"], after["PODLY_ADMIN_PASSWORD"])
	assert.Equal(t, before["PODLY_ADMIN_USERNAME"], after["PODLY_ADMIN_USERNAME"])
	assert.Equal(t, "true", after["REQUIRE_AUTH"])

	// The revoke leaves the file in the state PreStart expects, so the pass that
	// follows it converges instead of rewriting the file again.
	result, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, result.RestartNeeded, "PreStart must not fight the key revoke just wrote")
}

// TestRevokeSessionsBeforeTheFirstPassIsNotAnError: a revoke against an app that
// has never started has nothing to sign out, and failing it would park a node in
// ERROR for asking something already true.
func TestRevokeSessionsBeforeTheFirstPassIsNotAnError(t *testing.T) {
	cfg := newConfigurator(newStoreSecrets())
	require.NoError(t, cfg.RevokeSessions(context.Background(), testState(t)))
}

func TestNilStateIsAnErrorNotAPanic(t *testing.T) {
	cfg := newConfigurator(newStoreSecrets())
	ctx := context.Background()

	_, err := cfg.PreStart(ctx, nil)
	require.Error(t, err)
	require.Error(t, cfg.PostStart(ctx, nil))
	require.Error(t, cfg.RevokeSessions(ctx, nil))
}

func TestNameIsTheGraphNode(t *testing.T) {
	assert.Equal(t, nodeName, newConfigurator(nil).Name())
}

// TestEnvFileRoundTripsThroughTheRuntimeParser guards the format contract. The
// runtime splits the file on the first '=' and ignores blank and '#' lines; a
// value that only survives this package's own reader would be a config the
// container never receives.
func TestEnvFileRoundTripsThroughTheRuntimeParser(t *testing.T) {
	content := renderEnvFile("service-password", "session-key")
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "line the runtime would drop: %q", line)
		assert.NotEmpty(t, value, "line %q has an empty value", key)
		assert.NotContains(t, value, " ", "an unquoted value with a space would not survive the env-file parser")
	}
}
