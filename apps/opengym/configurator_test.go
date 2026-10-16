// SPDX-License-Identifier: AGPL-3.0-only

package opengym

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestConfigurator builds the API node's configurator against a fixed address.
func newTestConfigurator(origin string) *Configurator {
	return NewConfigurator(configurator.Deps{
		Address: configurator.AddressDeps{PrimaryBaseURL: func() string { return origin }},
	})
}

// readEnvFile reads back the file PreStart wrote as a map, so an assertion names the
// key it cares about instead of matching a whole document.
func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the env file the container spec declares must exist")

	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "env file line %q is not KEY=value", line)
		out[key] = value
	}
	return out
}

func TestPreStartWritesTheAddressTheAppMustRecognise(t *testing.T) {
	dir := t.TempDir()
	cfg := newTestConfigurator("https://bloud.example.com")

	res, err := cfg.PreStart(context.Background(), &configurator.AppState{DataPath: dir})
	require.NoError(t, err)
	// Nothing existed before this pass, so the container has to be recreated to pick
	// the values up.
	assert.True(t, res.RestartNeeded)

	env := readEnvFile(t, filepath.Join(dir, configDir, envFileName))
	// A passkey is bound to a hostname, never to a port, so RP_ID carries no :443 and
	// no scheme. The app lives on its own subdomain, so that subdomain is the name the
	// passkey is bound to. ORIGIN keeps scheme and port, because that is what the app
	// compares a request's own origin against.
	assert.Equal(t, "opengym.bloud.example.com", env["RP_ID"])
	assert.Equal(t, "https://opengym.bloud.example.com", env["ORIGIN"])
}

// TestPreStartFollowsTheLiveAddress pins the reason this configurator exists at all.
// The instance's address is a runtime setting, and a passkey registered under one
// hostname never works under another, so a saved address change has to reach the
// container or every sign-in becomes "verification failed".
func TestPreStartFollowsTheLiveAddress(t *testing.T) {
	dir := t.TempDir()
	origin := "http://bloud.localhost:8080"
	cfg := NewConfigurator(configurator.Deps{
		Address: configurator.AddressDeps{PrimaryBaseURL: func() string { return origin }},
	})

	_, err := cfg.PreStart(context.Background(), &configurator.AppState{DataPath: dir})
	require.NoError(t, err)

	origin = "https://home.example.com"
	res, err := cfg.PreStart(context.Background(), &configurator.AppState{DataPath: dir})
	require.NoError(t, err)
	require.True(t, res.RestartNeeded, "a moved address must recreate the container")

	env := readEnvFile(t, filepath.Join(dir, configDir, envFileName))
	assert.Equal(t, "opengym.home.example.com", env["RP_ID"])
	assert.Equal(t, "https://opengym.home.example.com", env["ORIGIN"])
}

// TestPreStartIsASteadyStateNoOp states the same rule the conformance harness holds,
// with the address the dev install actually runs on.
func TestPreStartIsASteadyStateNoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := newTestConfigurator("http://localhost:8080")
	state := &configurator.AppState{DataPath: dir}

	first, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)

	second, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a pass that changed nothing must restart nothing")
}

func TestRelyingPartyID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		want   string
	}{
		{"localhost with a port", "http://opengym.localhost:8080", "opengym.localhost"},
		{"https default port", "https://gym.example.com", "gym.example.com"},
		{"explicit port", "https://gym.example.com:8443", "gym.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := relyingPartyID(tc.origin)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := relyingPartyID("not a url")
	assert.Error(t, err, "an address with no hostname must fail rather than write an empty RP_ID")
}

func TestHealthIsOK(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"healthy", `{"ok":true,"users":0,"writable":true}`, true},
		{"store not writable", `{"ok":true,"users":0,"writable":false}`, false},
		{"not ok", `{"ok":false,"writable":true}`, false},
		// The signature upstream's own troubleshooting names for a catch-all route or
		// an auth proxy answering /api with a page instead of the API.
		{"html instead of json", "<!doctype html><html><body>openGym</body></html>", false},
		{"empty", "", false},
		{"missing writable", `{"ok":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, healthIsOK(200, []byte(tc.body)))
		})
	}
}

func TestNodeNamesAndPort(t *testing.T) {
	assert.Equal(t, apiNodeName, NewConfigurator(configurator.Deps{}).Name())

	web := NewWebConfigurator(0, configurator.Deps{})
	assert.Equal(t, webNodeName, web.Name())
	assert.Equal(t, defaultPort, web.Port(), "the default port must match metadata.yaml")
}
