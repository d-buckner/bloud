// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// inferenceState builds an app state carrying one instance inference
// binding, the shape the orchestrator produces from Settings -> AI.
func inferenceState(dataDir, endpoint, apiKey, model string) *configurator.AppState {
	return &configurator.AppState{
		DataPath: dataDir,
		Integrations: configurator.Integrations{
			Inference: []configurator.InferenceBinding{{
				Endpoint:     endpoint,
				APIKey:       apiKey,
				DefaultModel: model,
			}},
		},
	}
}

// readConfigFile reads back the config the configurator wrote.
func readConfigFile(t *testing.T, dataDir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "data", configFileName))
	require.NoError(t, err, "the configurator should have written the agent config")
	doc, err := parseConfig(raw)
	require.NoError(t, err)
	return doc
}

// bloudProvider pulls the Bloud provider entry out of a config document.
func bloudProvider(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	providers, ok := doc[inferenceProvidersKey].(map[string]any)
	require.True(t, ok, "config has no %q section", inferenceProvidersKey)
	entry, ok := providers[inferenceProviderKey].(map[string]any)
	require.True(t, ok, "config has no %q provider entry", inferenceProviderKey)
	return entry
}

// ---- inference wiring ----

func TestPreStartRegistersTheBloudProvider(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, SeedInstalledAgentSource(filepath.Join(dataDir, agentSourceDirName)))
	c := newTestConfigurator(t, nil)

	_, err := c.PreStart(context.Background(),
		inferenceState(dataDir, "https://ai.example.test/v1", "sk-test-123", "model-a"))
	require.NoError(t, err)

	entry := bloudProvider(t, readConfigFile(t, dataDir))
	assert.Equal(t, "Bloud", entry["name"])
	assert.Equal(t, inferenceAPIMode, entry["api_mode"])
	assert.Equal(t, "https://ai.example.test/v1", entry["base_url"])
	assert.Equal(t, "sk-test-123", entry["api_key"])
	assert.Equal(t, "model-a", entry["default_model"])
	assert.Equal(t, true, entry["discover_models"],
		"the agent should refresh its model list from the endpoint rather than trust a snapshot")

	model, ok := readConfigFile(t, dataDir)["model"].(map[string]any)
	require.True(t, ok, "config has no model selection")
	assert.Equal(t, inferenceProviderSlug, model["provider"])
	assert.Equal(t, "model-a", model["model"])
}

func TestPreStartAdoptsNoModelTheInstanceDidNotSupply(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, SeedInstalledAgentSource(filepath.Join(dataDir, agentSourceDirName)))
	c := newTestConfigurator(t, nil)

	_, err := c.PreStart(context.Background(), inferenceState(dataDir, "https://ai.example.test/v1", "k", ""))
	require.NoError(t, err)

	doc := readConfigFile(t, dataDir)
	assert.NotContains(t, bloudProvider(t, doc), "default_model",
		"an empty instance default must not be written as a model")
	assert.NotContains(t, doc, "model",
		"with no default to adopt, no model selection should be invented")
}

func TestPreStartLeavesAnOperatorChosenModelAlone(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, SeedInstalledAgentSource(filepath.Join(dataDir, agentSourceDirName)))
	cfgDir := filepath.Join(dataDir, "data")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, configFileName),
		[]byte("model:\n  provider: openrouter\n  model: human-picked-model\n"), 0o644))

	c := newTestConfigurator(t, nil)
	_, err := c.PreStart(context.Background(), inferenceState(dataDir, "https://ai.example.test/v1", "k", "model-a"))
	require.NoError(t, err)

	model := readConfigFile(t, dataDir)["model"].(map[string]any)
	assert.Equal(t, "openrouter", model["provider"],
		"a model chosen by the operator outranks the Bloud default")
	assert.Equal(t, "human-picked-model", model["model"])

	assert.Contains(t, bloudProvider(t, readConfigFile(t, dataDir)), "base_url",
		"the Bloud provider stays registered so switching back in the UI needs no re-typing")
}

func TestPreStartStripsOnlyWhatBloudWrote(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, SeedInstalledAgentSource(filepath.Join(dataDir, agentSourceDirName)))
	cfgDir := filepath.Join(dataDir, "data")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, configFileName), []byte(
		"providers:\n"+
			"  bloud:\n    base_url: https://old.test/v1\n"+
			"  other:\n    base_url: https://theirs.test/v1\n"+
			"model:\n  provider: custom:bloud\n  model: old-model\n"), 0o644))

	c := newTestConfigurator(t, nil)
	// No inference binding: the managed block and the selection Bloud made
	// both go away.
	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dataDir})
	require.NoError(t, err)

	doc := readConfigFile(t, dataDir)
	providers, ok := doc[inferenceProvidersKey].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, providers, inferenceProviderKey, "the Bloud entry should be stripped")
	assert.Contains(t, providers, "other", "a provider someone else added survives")

	assert.NotContains(t, doc, "model",
		"the selection Bloud wrote should be removed, and the empty section with it")
}

func TestPreStartKeepsAnOperatorModelWhenInferenceGoesAway(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, SeedInstalledAgentSource(filepath.Join(dataDir, agentSourceDirName)))
	cfgDir := filepath.Join(dataDir, "data")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, configFileName),
		[]byte("model:\n  provider: openrouter\n  model: theirs\n"), 0o644))

	c := newTestConfigurator(t, nil)
	_, err := c.PreStart(context.Background(), &configurator.AppState{DataPath: dataDir})
	require.NoError(t, err)

	model := readConfigFile(t, dataDir)["model"].(map[string]any)
	assert.Equal(t, "openrouter", model["provider"],
		"removing Bloud's inference must not delete a selection Bloud did not write")
}

func TestPreStartConfigConvergesAfterOneWrite(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, SeedInstalledAgentSource(filepath.Join(dataDir, agentSourceDirName)))
	c := newTestConfigurator(t, nil)
	state := inferenceState(dataDir, "https://ai.example.test/v1", "k", "model-a")

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, first.RestartNeeded,
		"a config write needs no restart: the app re-reads config.yaml rather than caching it at boot")

	cfgPath := filepath.Join(dataDir, "data", configFileName)
	before, err := os.ReadFile(cfgPath)
	require.NoError(t, err)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a second pass over unchanged intent must ask for nothing")

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the file must not be rewritten on a no-op pass")
}

// TestWriteConfigDefersWhenTheHostCannotWriteAndMayNotExec covers the order
// the two phases depend on: PreStart meets a container-owned directory and
// leaves it alone rather than failing the pass, because the container it
// would have to exec into may not exist yet.
func TestWriteConfigDefersWhenTheHostCannotWriteAndMayNotExec(t *testing.T) {
	c := newTestConfigurator(t, nil)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("x: 1\n"), 0o600))
	require.NoError(t, os.Chmod(filepath.Dir(cfgPath), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(cfgPath), 0o755) })

	changed, err := c.writeConfig(context.Background(), cfgPath, []byte("y: 2\n"), false)
	require.NoError(t, err, "a refused host write is not a failed pass when exec is not authorized")
	assert.False(t, changed)
}

// TestWriteConfigGoesThroughTheContainerWhenAuthorized is the whole reason
// PostStart owns the config convergence: once the agent owns its home, the
// container is the only writer, and the payload has to reach it encoded.
func TestWriteConfigGoesThroughTheContainerWhenAuthorized(t *testing.T) {
	var gotCmd []string
	c := newTestConfigurator(t, nil)
	c.exec = func(_ context.Context, container string, _ map[string]string, cmd []string) ([]byte, error) {
		gotCmd = append([]string{container}, cmd...)
		return nil, nil
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("x: 1\n"), 0o600))
	require.NoError(t, os.Chmod(filepath.Dir(cfgPath), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(cfgPath), 0o755) })

	want := []byte("providers:\n  bloud:\n    base_url: https://x.test/v1\n")
	changed, err := c.writeConfig(context.Background(), cfgPath, want, true)
	require.NoError(t, err)
	assert.True(t, changed, "a write that lands inside the container is a change")

	assert.Equal(t, nodeName, gotCmd[0], "the write must target this app's own container")
	require.Len(t, gotCmd, 4)
	assert.Equal(t, "sh", gotCmd[1])
	assert.Equal(t, "-c", gotCmd[2])

	decoded := decodeExecPayload(t, gotCmd[3])
	assert.Equal(t, string(want), decoded,
		"the document must arrive byte-for-byte through the base64 channel")
}

// decodeExecPayload pulls the payload back out of the shell command the
// configurator built, so the test asserts on the bytes the container would
// write rather than on the command's shape.
func decodeExecPayload(t *testing.T, script string) string {
	t.Helper()
	match := regexp.MustCompile(`printf %s '([A-Za-z0-9+/=]+)'`).FindStringSubmatch(script)
	require.Len(t, match, 2, "the script should carry a base64 payload: %s", script)
	raw, err := base64.StdEncoding.DecodeString(match[1])
	require.NoError(t, err)
	return string(raw)
}

func TestWriteConfigReportsARefusalWithNoExecChannel(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{})
	c.exec = nil
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("x: 1\n"), 0o600))
	require.NoError(t, os.Chmod(filepath.Dir(cfgPath), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(cfgPath), 0o755) })

	_, err := c.writeConfig(context.Background(), cfgPath, []byte("y: 2\n"), true)
	require.Error(t, err, "with no exec channel a refused write has nowhere to go")
	assert.Contains(t, err.Error(), "INTEGRATION.md")
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

// jsonHandler serves one JSON document at one path and 404 elsewhere.
func jsonHandler(t *testing.T, path string, payload any) http.Handler {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

func TestPostStartRejectsAnUnhealthyApp(t *testing.T) {
	c := newTestConfigurator(t, jsonHandler(t, healthPath, healthResponse{Status: "degraded"}))
	err := c.PostStart(context.Background(), nil)
	require.Error(t, err, "an app that reports itself unhealthy must not pass verification")
	assert.Contains(t, err.Error(), "degraded")
}

func TestPostStartToleratesAnUnreachableApp(t *testing.T) {
	c := newTestConfigurator(t, nil)
	require.NoError(t, c.PostStart(context.Background(), nil),
		"a transport failure right after the health check passed is a race, not a fault")
}

func TestPostStartReportsTheWiredProfile(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case healthPath:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case profilesPath:
			_, _ = w.Write([]byte(`{"active":"default","profiles":[{"name":"default","is_default":true,` +
				`"is_active":true,"provider":"custom:bloud","model":"model-a"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	logger, buf := logSink(t)
	c := newTestConfigurator(t, handler)
	c.logger = logger

	dataDir := t.TempDir()
	require.NoError(t, c.PostStart(context.Background(), inferenceState(dataDir, "https://x.test/v1", "k", "model-a")))
	assert.Contains(t, buf.String(), "wired to Bloud's model")
	assert.Contains(t, buf.String(), "model-a")
}

func TestPostStartWarnsWhenTheProfileIsNotBlouds(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case healthPath:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case profilesPath:
			_, _ = w.Write([]byte(`{"active":"default","profiles":[{"name":"default","is_default":true,` +
				`"is_active":true,"provider":"openrouter","model":"theirs"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	logger, buf := logSink(t)
	c := newTestConfigurator(t, handler)
	c.logger = logger

	require.NoError(t, c.PostStart(context.Background(),
		inferenceState(t.TempDir(), "https://x.test/v1", "k", "model-a")),
		"an operator-chosen model is not an error")
	assert.Contains(t, buf.String(), "not using the Bloud provider")
}

func TestProviderIsBloud(t *testing.T) {
	assert.True(t, providerIsBloud("bloud"))
	assert.True(t, providerIsBloud("custom:bloud"))
	assert.True(t, providerIsBloud("  Custom:Bloud  "))
	assert.False(t, providerIsBloud("openrouter"))
	assert.False(t, providerIsBloud(""))
	assert.False(t, providerIsBloud("bloud-but-not-really"))
}

// ---- config document helpers ----

func TestParseConfigTreatsEmptyAsEmptyDocument(t *testing.T) {
	for _, raw := range []string{"", "   \n", "\n# only a comment\n"} {
		doc, err := parseConfig([]byte(raw))
		require.NoError(t, err)
		assert.NotNil(t, doc)
		assert.Empty(t, doc)
	}
}

func TestConvergeConfigPreservesUnrelatedKeys(t *testing.T) {
	dataDir := t.TempDir()
	cfgDir := filepath.Join(dataDir, "data")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, configFileName),
		[]byte("memory:\n  enabled: true\nuser:\n  name: someone\n"), 0o644))

	c := newTestConfigurator(t, nil)
	changed, err := c.convergeConfig(context.Background(), dataDir,
		inferenceState(dataDir, "https://x.test/v1", "k", "m"), false)
	require.NoError(t, err)
	assert.True(t, changed)

	doc := readConfigFile(t, dataDir)
	assert.Equal(t, map[string]any{"enabled": true}, doc["memory"])
	assert.Equal(t, map[string]any{"name": "someone"}, doc["user"])
	assert.Contains(t, bloudProvider(t, doc), "base_url")

	// And a second pass over the same intent writes nothing.
	changed, err = c.convergeConfig(context.Background(), dataDir,
		inferenceState(dataDir, "https://x.test/v1", "k", "m"), false)
	require.NoError(t, err)
	assert.False(t, changed)
}

// TestConfigRoundTripsThroughYAML guards the assumption the whole merge rests
// on: that marshaling the parsed document back out re-parses to the same
// document, which is what makes "changed" mean a real difference.
func TestConfigRoundTripsThroughYAML(t *testing.T) {
	doc := map[string]any{}
	applyInference(doc, configurator.InferenceBinding{
		Endpoint:     "https://x.test/v1",
		APIKey:       "k",
		DefaultModel: "m",
	})
	out, err := yaml.Marshal(doc)
	require.NoError(t, err)

	again, err := parseConfig(out)
	require.NoError(t, err)
	assert.Equal(t, doc, again)
}

// TestVersionMarkerNamesBothPinHalves keeps the marker honest: it has to
// carry the tag and the digest, or a bump of one leaves the other unable to
// invalidate the installed tree.
func TestVersionMarkerNamesBothPinHalves(t *testing.T) {
	assert.Contains(t, versionMarker, agentSourceTag)
	assert.Contains(t, versionMarker, agentSourceSHA256)
}

// TestAgentSourceURLCarriesTheTag guards against a URL that drifted off its
// own constant, which would pin one version in the marker and fetch another.
func TestAgentSourceURLCarriesTheTag(t *testing.T) {
	assert.Contains(t, agentSourceURL, agentSourceTag)
	assert.Contains(t, agentSourceURL, "NousResearch/hermes-agent")
}

// TestDefaultPortMatchesRegistration is a local restatement of what the
// conformance harness checks against metadata, kept here so a change in this
// package fails fast in its own test run.
func TestDefaultPortMatchesRegistration(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{})
	assert.Equal(t, defaultPort, c.port)
	assert.Equal(t, nodeName, c.Name())
	assert.Equal(t, fmt.Sprintf("http://localhost:%d", defaultPort),
		fmt.Sprintf("http://localhost:%d", c.port))
}
