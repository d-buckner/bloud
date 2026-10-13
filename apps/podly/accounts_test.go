// SPDX-License-Identifier: AGPL-3.0-only

package podly

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// newAppConfigurator wires a configurator to a running fake instead of to the
// default localhost address.
func newAppConfigurator(t *testing.T, secrets configurator.AppSecretsProvider, srv *httptest.Server) *Configurator {
	t.Helper()
	return &Configurator{
		port:    defaultPort,
		secrets: secrets,
		api:     newAPI(configurator.ClientFactory{}, func() string { return srv.URL }),
		logger:  quietLogger(),
	}
}

func startFake(t *testing.T, fake *fakePodly) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return srv
}

// TestPostStartCreatesTheSignInAccountForTheRevealedPassword is the whole point
// of the contract: the value Bloud reveals and the value the app accepts have to
// be one value, and the only way to make that true after first boot is the app's
// own user API.
func TestPostStartCreatesTheSignInAccountForTheRevealedPassword(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	cfg := newAppConfigurator(t, secrets, srv)
	state := testState(t)

	require.NoError(t, cfg.PostStart(context.Background(), state))

	minted := secrets.GetAppSecret(appName, clientPasswordSecret)
	require.NotEmpty(t, minted, "the revealed credential has to be the one the app holds")

	held, ok := fake.passwordOf(humanUser)
	require.True(t, ok, "the sign-in account was not created")
	assert.Equal(t, minted, held)

	assert.Equal(t, adminRole, fake.roleOf(humanUser),
		"a non-admin hits the feed allowance and cannot manage users")
}

// TestSteadyStatePostStartWritesNothing is the resync contract for the API half:
// the pass still reads the user list, because that read is how a deleted account
// gets noticed, but it issues no write and no password change.
func TestSteadyStatePostStartWritesNothing(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	cfg := newAppConfigurator(t, secrets, srv)
	state := testState(t)

	require.NoError(t, cfg.PostStart(context.Background(), state))
	minted := secrets.GetAppSecret(appName, clientPasswordSecret)

	for i := 0; i < 3; i++ {
		fake.reset()
		require.NoError(t, cfg.PostStart(context.Background(), state))
		assert.Zero(t, fake.count("POST", usersPath), "pass %d created a user that already exists", i)
		assert.Zero(t, fake.count("PATCH", usersPath+"/"+humanUser), "pass %d re-set a password nobody changed", i)
		assert.Equal(t, minted, secrets.GetAppSecret(appName, clientPasswordSecret), "the credential must be minted once")
	}
	assert.NotZero(t, fake.count("GET", usersPath), "a pass that never looked cannot notice a deleted account")
}

// TestPostStartAppliesARotation is what makes `rotate: bloud` more than a label.
// The environment cannot carry a rotated password to an app whose bootstrap only
// runs on an empty user table, so the rotation has to arrive as an API call.
func TestPostStartAppliesARotation(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	cfg := newAppConfigurator(t, secrets, srv)
	state := testState(t)

	require.NoError(t, cfg.PostStart(context.Background(), state))

	rotated := secrets.rotate("a-completely-new-password")
	require.NoError(t, cfg.PostStart(context.Background(), state))

	held, ok := fake.passwordOf(humanUser)
	require.True(t, ok)
	assert.Equal(t, rotated, held, "the app still holds the pre-rotation password")
	assert.Equal(t, 1, fake.count("PATCH", usersPath+"/"+humanUser))

	// And the new state is itself stable: the rotation is applied once, not
	// re-sent on every pass until something else changes.
	fake.reset()
	require.NoError(t, cfg.PostStart(context.Background(), state))
	assert.Zero(t, fake.count("PATCH", usersPath+"/"+humanUser))
}

// TestPostStartRecreatesADeletedAccount: the app's own UI lets an admin delete a
// user, including the one Bloud maintains. Intent beats history, so the next
// pass puts it back rather than leaving a revealed password pointing at nobody.
func TestPostStartRecreatesADeletedAccount(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	cfg := newAppConfigurator(t, secrets, srv)
	state := testState(t)

	require.NoError(t, cfg.PostStart(context.Background(), state))
	require.NoError(t, cfg.PostStart(context.Background(), state))

	fake.removeUser(humanUser)
	require.NoError(t, cfg.PostStart(context.Background(), state))
	assert.True(t, fake.hasUser(humanUser))
	held, _ := fake.passwordOf(humanUser)
	assert.Equal(t, secrets.GetAppSecret(appName, clientPasswordSecret), held)
}

// TestPostStartLeavesAnOpenAppAlone: with REQUIRE_AUTH off the app answers 404
// on every auth route and serves its feeds to anyone. Bloud cannot fix that from
// outside, and creating accounts against an app that has no accounts would
// report success while meaning nothing, so the pass warns and stops.
func TestPostStartLeavesAnOpenAppAlone(t *testing.T) {
	fake := newFakePodly()
	fake.requireAuth = false
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, newStoreSecrets(), srv)

	require.NoError(t, cfg.PostStart(context.Background(), testState(t)))
	assert.Zero(t, fake.count("POST", loginPath))
	assert.False(t, fake.hasUser(humanUser))
}

// TestPostStartWhenTheServicePasswordNoLongerWorks covers the one failure the
// design cannot recover from: the app's copy of the service password was changed
// inside the app, and setting a password requires an authenticated admin. The
// pass must not fail the node over it, because the app is up and its feeds work.
func TestPostStartWhenTheServicePasswordNoLongerWorks(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	cfg := newAppConfigurator(t, secrets, srv)

	fake.users[serviceUser] = "changed-inside-the-app"
	require.NoError(t, cfg.PostStart(context.Background(), testState(t)))

	assert.False(t, fake.hasUser(humanUser), "no write was authorized, so none should have been attempted")
	assert.Zero(t, fake.count("POST", usersPath))
}

// TestPostStartWithoutASecretStoreDoesNotReachTheApp: no store means no
// credential to deliver, and a login attempt with an empty password would only
// trip the app's failed-login backoff.
func TestPostStartWithoutASecretStoreDoesNotReachTheApp(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	cfg := newAppConfigurator(t, nil, srv)

	require.NoError(t, cfg.PostStart(context.Background(), testState(t)))
	assert.Zero(t, fake.total())
}

// TestPostStartTakesThePasswordTheStoreAlreadyHolds pins that a password minted
// before a restart is reused: the reveal shows the stored value, so a second
// mint would show a password the app has never heard of.
func TestPostStartTakesThePasswordTheStoreAlreadyHolds(t *testing.T) {
	fake := newFakePodly()
	srv := startFake(t, fake)
	secrets := newStoreSecrets()
	secrets.rotate("password-from-an-earlier-install")
	cfg := newAppConfigurator(t, secrets, srv)

	require.NoError(t, cfg.PostStart(context.Background(), testState(t)))

	held, ok := fake.passwordOf(humanUser)
	require.True(t, ok)
	assert.Equal(t, "password-from-an-earlier-install", held)
}
