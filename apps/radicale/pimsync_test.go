// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pimsyncTargetForTest() pimsyncTarget {
	return pimsyncTarget{
		URL:      "http://apps-radicale:5232/calendar-service/",
		Username: "calendar-service",
		Password: "owner-secret",
	}
}

// ---- scfg quoting ----

func TestScfgQuoteQuotesEverything(t *testing.T) {
	got, err := scfgQuote("hello")
	require.NoError(t, err)
	assert.Equal(t, `"hello"`, got)
}

func TestScfgQuoteEscapesTheTwoCharactersScfgCannotCarry(t *testing.T) {
	// scfg has no escape sequences: a backslash escapes whatever follows it,
	// so the only two characters that cannot appear literally inside a
	// double-quoted string are the backslash and the quote itself.
	got, err := scfgQuote(`a"b\c`)
	require.NoError(t, err)
	assert.Equal(t, `"a\"b\\c"`, got)
}

func TestScfgQuoteRoundTripsAGeneratedSecret(t *testing.T) {
	// A generated credential is the thing most likely to break a config
	// format, so the characters that survive are the ones a random base64-ish
	// alphabet actually produces.
	const secret = "Kx7p+q2r/s=t&v?w#y-z_1m"
	got, err := scfgQuote(secret)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, `"`) && strings.HasSuffix(got, `"`))
	assert.NotContains(t, got, "\n")
}

func TestScfgQuoteRefusesControlCharacters(t *testing.T) {
	// scfg rejects control characters outright rather than mangling them, so
	// refusing here is what keeps a bad value from producing a config file
	// that fails to parse at the far end with no clue where it came from.
	for _, bad := range []string{"a\nb", "a\rb", "a\tb", "a\x00b"} {
		_, err := scfgQuote(bad)
		require.Error(t, err, "value %q must be refused", bad)
	}
}

// ---- config rendering ----

func TestRenderPimsyncConfProjectsEachFeedIntoTheTarget(t *testing.T) {
	got, err := renderPimsyncConf(pimsyncTargetForTest(), []configurator.ICSFeedBinding{feedBinding()})
	require.NoError(t, err)

	// The CalDAV target: the shared calendar account inside this Radicale.
	assert.Contains(t, got, "storage bloud {")
	assert.Contains(t, got, "    type caldav")
	assert.Contains(t, got, `    url "http://apps-radicale:5232/calendar-service/"`)
	assert.Contains(t, got, `    username "calendar-service"`)
	assert.Contains(t, got, `    password "owner-secret"`)

	// The feed: read-only, named after the app that publishes it.
	assert.Contains(t, got, "storage radarr {")
	assert.Contains(t, got, "    type webcal")
	assert.Contains(t, got,
		`    url "http://apps-radarr:7878/feed/v3/calendar/Radarr.ics?apikey=abc123"`)
	assert.Contains(t, got, `    collection_id "radarr"`)

	// The pair: one-way, feed first. one_way is what makes the target a
	// projection rather than a merge, so a feed never produces a conflict
	// nobody is around to resolve.
	assert.Contains(t, got, "pair radarr {")
	assert.Contains(t, got, "    storage_a radarr")
	assert.Contains(t, got, "    storage_b bloud")
	assert.Contains(t, got, `    collection "radarr"`)
	assert.Contains(t, got, "    one_way")
}

func TestRenderPimsyncConfParksWithoutAnOwnerCredential(t *testing.T) {
	// No credential means nothing can be written to the target. Rendering the
	// pairs anyway would have the daemon fail on every connection; rendering
	// none means the sidecar sits idle until a later pass has a real target.
	target := pimsyncTargetForTest()
	target.Password = ""

	got, err := renderPimsyncConf(target, []configurator.ICSFeedBinding{feedBinding()})
	require.NoError(t, err)
	assert.NotContains(t, got, "pair ")
	assert.NotContains(t, got, "storage radarr")
	assert.Contains(t, got, "status_path", "the config is still a valid file")
}

func TestRenderPimsyncConfSkipsIncompleteFeeds(t *testing.T) {
	cases := map[string]func(*configurator.ICSFeedBinding){
		"not installed": func(f *configurator.ICSFeedBinding) { f.Installed = false },
		"no key":        func(f *configurator.ICSFeedBinding) { f.APIKey = "" },
		"no path":       func(f *configurator.ICSFeedBinding) { f.Path = "" },
		"no address":    func(f *configurator.ICSFeedBinding) { f.BaseURL = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			feed := feedBinding()
			mutate(&feed)
			got, err := renderPimsyncConf(pimsyncTargetForTest(), []configurator.ICSFeedBinding{feed})
			require.NoError(t, err)
			assert.NotContains(t, got, "pair ")
		})
	}
}

func TestRenderPimsyncConfIsDeterministic(t *testing.T) {
	radarr := feedBinding()
	sonarr := feedBinding()
	sonarr.App = "sonarr"
	sonarr.Path = "/feed/v3/calendar/Sonarr.ics"

	first, err := renderPimsyncConf(pimsyncTargetForTest(), []configurator.ICSFeedBinding{radarr, sonarr})
	require.NoError(t, err)
	second, err := renderPimsyncConf(pimsyncTargetForTest(), []configurator.ICSFeedBinding{sonarr, radarr})
	require.NoError(t, err)

	assert.Equal(t, first, second, "block order must not depend on binding order")
	assert.Less(t, strings.Index(first, "pair radarr"), strings.Index(first, "pair sonarr"))
}

func TestRenderPimsyncConfRefusesASecretScfgCannotHold(t *testing.T) {
	target := pimsyncTargetForTest()
	target.Password = "has\nnewline"
	_, err := renderPimsyncConf(target, nil)
	require.Error(t, err)
}

// ---- the sidecar's PreStart ----

func newPimsyncConfigurator(t *testing.T, secrets configurator.AppSecretsProvider) *PimsyncConfigurator {
	t.Helper()
	return NewPimsyncConfigurator(configurator.Deps{Logger: quietLogger(), Secrets: secrets})
}

// ownerSecrets stands in for the credential the Authentik configurator
// publishes into this app's own secret scope during its convergence.
func pimsyncOwnerSecrets(value string) *fakeSecrets {
	return &fakeSecrets{values: map[string]string{
		appName + "/" + authentik.CalendarOwnerSecretKey: value,
	}}
}

func TestPimsyncPreStartWritesTheConfigTheSidecarMounts(t *testing.T) {
	dataPath := t.TempDir()
	c := newPimsyncConfigurator(t, pimsyncOwnerSecrets("owner-secret"))
	state := &configurator.AppState{DataPath: dataPath}
	state.Integrations.ICSFeeds = []configurator.ICSFeedBinding{feedBinding()}

	result, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, result.RestartNeeded, "a fresh config needs the container recreated to read it")

	raw, err := os.ReadFile(filepath.Join(dataPath, pimsyncConfigDirName, pimsyncConfigFileName))
	require.NoError(t, err, "the config must land where the volume mount expects it")
	assert.Contains(t, string(raw), "pair radarr")
	assert.Contains(t, string(raw), `    password "owner-secret"`)
}

func TestPimsyncPreStartIsIdempotent(t *testing.T) {
	dataPath := t.TempDir()
	c := newPimsyncConfigurator(t, pimsyncOwnerSecrets("owner-secret"))
	state := &configurator.AppState{DataPath: dataPath}
	state.Integrations.ICSFeeds = []configurator.ICSFeedBinding{feedBinding()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	result, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, result.RestartNeeded, "an unchanged config must not recreate the container")
}

func TestPimsyncPreStartRestartsWhenAFeedIsAdded(t *testing.T) {
	// The sidecar is the node that reacts to feeds. Radicale's own node no
	// longer sees them at all: it serves whatever the sidecar wrote.
	dataPath := t.TempDir()
	c := newPimsyncConfigurator(t, pimsyncOwnerSecrets("owner-secret"))
	state := &configurator.AppState{DataPath: dataPath}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	state.Integrations.ICSFeeds = []configurator.ICSFeedBinding{feedBinding()}
	result, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, result.RestartNeeded, "a new feed means a new pair, which the daemon only reads at boot")
}

func TestPimsyncPreStartOpensTheStatusDirForTheContainerUser(t *testing.T) {
	// The image runs as uid 1000, which under rootless podman is a
	// subordinate uid the host agent is not and cannot become, so the agent
	// opens the directory rather than owning it.
	dataPath := t.TempDir()
	c := newPimsyncConfigurator(t, nil)
	state := &configurator.AppState{DataPath: dataPath}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dataPath, pimsyncStatusDirName))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.EqualValues(t, 0o777, info.Mode().Perm()&0o777)
}

func TestPimsyncPreStartWithoutASecretRendersAParkedConfig(t *testing.T) {
	// The credential is published by the Authentik configurator during its
	// own convergence. Before that lands, the honest render is a config with
	// no pairs, not a failure: the next pass has more information.
	dataPath := t.TempDir()
	c := newPimsyncConfigurator(t, nil)
	state := &configurator.AppState{DataPath: dataPath}
	state.Integrations.ICSFeeds = []configurator.ICSFeedBinding{feedBinding()}

	_, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(dataPath, pimsyncConfigDirName, pimsyncConfigFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "pair ")
}

func TestPimsyncPreStartToleratesNoDataPath(t *testing.T) {
	c := newPimsyncConfigurator(t, nil)
	result, err := c.PreStart(context.Background(), &configurator.AppState{})
	require.NoError(t, err)
	assert.False(t, result.RestartNeeded)
}

func TestPimsyncConfiguratorNameIsTheSidecarNode(t *testing.T) {
	assert.Equal(t, pimsyncNodeName, newPimsyncConfigurator(t, nil).Name())
}

func TestPimsyncPostStartIsANoOp(t *testing.T) {
	// The sidecar's useful state is the sync itself, asserted end to end by
	// the aggregation integration test rather than re-derived here.
	require.NoError(t, newPimsyncConfigurator(t, nil).PostStart(context.Background(), nil))
}
