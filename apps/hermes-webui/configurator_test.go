// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- helpers ----

// logSink is a configurator logger whose records land in a buffer, so what
// the configurator chose to say is under test alongside what it chose to do.
func logSink(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

// newTestConfigurator builds a configurator whose client resolves to the
// given handler instead of the real app port, and whose asset installer is
// pointed at a cache dir that holds nothing. A nil handler means nothing is
// listening, which is how the unreachable case is exercised.
func newTestConfigurator(t *testing.T, app http.Handler) *Configurator {
	t.Helper()
	logger, _ := logSink(t)
	c := NewConfigurator(0, configurator.Deps{
		Logger: logger,
		Assets: appasset.Installer{CacheDir: t.TempDir()},
	})
	if app != nil {
		server := httptest.NewServer(app)
		t.Cleanup(server.Close)
		c.baseURL = server.URL
	}
	c.api.cl.WithSleeper(func(time.Duration) {})
	return c
}

// appHandler serves a fixed document per path and 404 elsewhere, so a pass
// can be run against a stand-in for the whole app surface.
func appHandler(documents map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := documents[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// healthy is the baseline every PostStart test starts from.
const healthy = `{"status":"ok"}`

// ---- ownership of the agent config ----

// TestPreStartWritesNoAgentConfig is the ownership assertion the shared
// home depends on. apps/hermes owns config.yaml; this app only reads it.
// A second writer on one file is a churn loop, and worse, it would let the
// front end silently overwrite what the agent's own convergence produced.
func TestPreStartWritesNoAgentConfig(t *testing.T) {
	dataDir := t.TempDir()
	c := newTestConfigurator(t, nil)

	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dataDir})
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dataDir, "data", "config.yaml"))
	assert.True(t, os.IsNotExist(statErr),
		"the web UI must not write the agent config; Hermes owns the shared $HERMES_HOME")
}

// ---- agent source ----

func TestAgentSourceSkipIsVersionAware(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, agentSourceInstalled(dir), "an empty dir is not an installed source")

	require.NoError(t, SeedInstalledAgentSource(dir))
	assert.True(t, agentSourceInstalled(dir), "the seeded marker names this build's pin")

	require.NoError(t, os.WriteFile(filepath.Join(dir, versionMarkerName),
		[]byte("hermes-agent v2020.1.1 deadbeef\n"), 0o644))
	assert.False(t, agentSourceInstalled(dir),
		"a marker for another tag or digest must not pass, or a bump would leave the old tree mounted")
}

// TestEnsureReadableRootOpensATraversalBlockedTree is the regression for
// the failure that killed the app at import time: MkdirAll(0755) under a
// 0077 umask lands 0700, and the container's uid cannot traverse into its
// own agent tree. The symptom is a PermissionError on run_agent.py from
// inside a container that installed cleanly, so the mode is asserted here.
func TestEnsureReadableRootOpensATraversalBlockedTree(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))

	c := newTestConfigurator(t, nil)
	require.NoError(t, c.ensureReadableRoot(dir))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(),
		"group and other need read+execute or the container cannot enter the mount")

	// And it is a no-op the second time, so it is safe on every pass.
	require.NoError(t, c.ensureReadableRoot(dir))
	info, err = os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestEnsureReadableRootReportsAMissingTree(t *testing.T) {
	c := newTestConfigurator(t, nil)
	err := c.ensureReadableRoot(filepath.Join(t.TempDir(), "absent"))
	require.Error(t, err, "a missing tree is not silently made readable")
}

// TestMakeAgentSourceReadableCoversTheWholeTree guards the wider case: an
// archive that carried restrictive modes would leave the container reading
// its agent one file at a time, failing on whichever one it got to.
func TestMakeAgentSourceReadableCoversTheWholeTree(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "agent", "core")
	require.NoError(t, os.MkdirAll(nested, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "run_agent.py"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte("y"), 0o600))

	require.NoError(t, makeAgentSourceReadable(root))

	for _, dir := range []string{root, filepath.Join(root, "agent"), nested} {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode().Perm()&0o055, "dir %s must be readable and traversable", dir)
	}
	for _, f := range []string{
		filepath.Join(nested, "run_agent.py"),
		filepath.Join(root, "pyproject.toml"),
	} {
		info, err := os.Stat(f)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode().Perm()&0o004, "file %s must be readable by the container", f)
	}
}

func TestVerifyAgentSourceRejectsAWrongTree(t *testing.T) {
	dir := t.TempDir()
	err := verifyAgentSource(dir)
	require.Error(t, err, "an empty tree is not a Hermes agent checkout")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "run_agent.py"), []byte("#!python\n"), 0o644))
	require.Error(t, verifyAgentSource(dir), "run_agent.py alone is not enough")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644))
	assert.NoError(t, verifyAgentSource(dir))
}

func TestPreStartWithoutAnAssetInstallerDoesNotFail(t *testing.T) {
	dataDir := t.TempDir()
	logger, buf := logSink(t)
	c := NewConfigurator(0, configurator.Deps{Logger: logger})
	require.False(t, c.canInstallAssets(), "a zero-value installer has no cache dir")

	res, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dataDir})
	require.NoError(t, err, "a context with no host installer is not a failed pass")
	assert.False(t, res.RestartNeeded)
	assert.Contains(t, buf.String(), "no asset installer")
}

// ---- post-start ----

func TestPostStartRejectsAnUnhealthyApp(t *testing.T) {
	c := newTestConfigurator(t, appHandler(map[string]string{healthPath: `{"status":"degraded"}`}))
	err := c.PostStart(context.Background(), nil)
	require.Error(t, err, "an app that reports itself unhealthy must not pass verification")
	assert.Contains(t, err.Error(), "degraded")
}

func TestPostStartToleratesAnUnreachableApp(t *testing.T) {
	c := newTestConfigurator(t, nil)
	require.NoError(t, c.PostStart(context.Background(), nil),
		"a transport failure right after the health check passed is a race, not a fault")
}

// TestPostStartReportsTheSharedProfile is the read-back of the shared home:
// the profile the running server resolved is the shared config.yaml as the
// agent read it, so logging it is what makes "one brain, two surfaces"
// visible rather than assumed.
func TestPostStartReportsTheSharedProfile(t *testing.T) {
	c := newTestConfigurator(t, appHandler(map[string]string{
		healthPath: healthy,
		profilesPath: `{"active":"default","profiles":[{"name":"default","is_default":true,` +
			`"is_active":true,"provider":"custom:bloud","model":"model-a"}]}`,
	}))
	logger, buf := logSink(t)
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(), nil))
	assert.Contains(t, buf.String(), "shared agent profile")
	assert.Contains(t, buf.String(), "model-a")
	assert.Contains(t, buf.String(), "custom:bloud")
}

// TestPostStartReportsTheSharedMCPServers is the assertion behind the whole
// design: an MCP namespace written for Hermes shows up here with no wiring
// on this side. The web UI reads the same config.yaml, so the namespaces
// and their live state come straight through its own endpoint.
func TestPostStartReportsTheSharedMCPServers(t *testing.T) {
	c := newTestConfigurator(t, appHandler(map[string]string{
		healthPath: healthy,
		mcpServersPath: `{"servers":[{"name":"affine-mcp","transport":"http","enabled":true,` +
			`"active":true,"status":"active"},{"name":"other","transport":"http","enabled":true,` +
			`"active":false,"status":"configured"}]}`,
	}))
	logger, buf := logSink(t)
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(), nil))
	assert.Contains(t, buf.String(), "MCP namespaces")
	assert.Contains(t, buf.String(), "affine-mcp=active")
	assert.Contains(t, buf.String(), "other=configured")
}

// TestPostStartNotesAnEmptyMCPHome keeps the empty case a statement rather
// than a silence or a failure. Hermes' `mcp` contract is optional, so an
// instance with no MCP-capable app has nothing to share, and that is a
// valid state an operator should be told about in one line.
func TestPostStartNotesAnEmptyMCPHome(t *testing.T) {
	c := newTestConfigurator(t, appHandler(map[string]string{
		healthPath:     healthy,
		mcpServersPath: `{"servers":[]}`,
	}))
	logger, buf := logSink(t)
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(), nil))
	assert.Contains(t, buf.String(), "carries no MCP servers")
}

// TestPostStartToleratesAnUnreadableMCPList keeps a missing endpoint from
// failing a node that is otherwise healthy. The report is diagnostics; an
// older image without the route must not turn into a terminal ERROR.
func TestPostStartToleratesAnUnreadableMCPList(t *testing.T) {
	c := newTestConfigurator(t, appHandler(map[string]string{healthPath: healthy}))
	logger, buf := logSink(t)
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(), nil))
	assert.Contains(t, buf.String(), "could not read the MCP server list")
}

// ---- response shapes ----

func TestProfilesActivePrefersTheActiveFlag(t *testing.T) {
	raw := `{"active":"b","profiles":[` +
		`{"name":"a","is_default":true,"is_active":false},` +
		`{"name":"b","is_default":false,"is_active":true}]}`
	var resp profilesResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))

	active, ok := resp.active()
	require.True(t, ok)
	assert.Equal(t, "b", active.Name)
}

func TestMcpServerSummaryNamesState(t *testing.T) {
	resp := mcpServersResponse{Servers: []mcpServerEntry{
		{Name: "affine-mcp", Status: "active"},
		{Name: "gone", Status: "disabled"},
	}}
	assert.Equal(t, []string{"affine-mcp=active", "gone=disabled"}, mcpServerSummary(resp))
}
