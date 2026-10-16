// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// memoryState builds an app state carrying the given memory bindings.
func memoryState(dir string, memories ...bindings.MemoryBinding) *configurator.AppState {
	st := &configurator.AppState{DataPath: dir}
	st.Integrations.Memories = memories
	return st
}

// hindsightBinding is the shipped provider's shape: the address the host
// loopback serves, the bearer its API checks, and the three facts that place the
// plugin.
func hindsightBinding() bindings.MemoryBinding {
	return bindings.MemoryBinding{
		ProviderRef: bindings.ProviderRef{
			App:       "hindsight",
			Installed: true,
			BaseURL:   "http://apps-hindsight-gate:9999",
			LocalURL:  "http://localhost:9999",
		},
		Token:            "memory-bearer",
		PluginName:       "hindsight",
		PluginCommit:     "d952b6e655fccab4581ee0722b51c5f103873d12",
		PluginConfigPath: "hindsight/config.json",
	}
}

func memoryHermes() *Configurator {
	return NewConfigurator(0, configurator.Deps{
		Logger: quietLogger(),
		Address: configurator.AddressDeps{
			PrimaryBaseURL: func() string { return "http://localhost:8080" },
		},
	})
}

func readMemoryConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "data", "hindsight", "config.json"))
	if err != nil {
		t.Fatalf("reading the memory plugin config: %v", err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the written memory plugin config is not valid json: %v", err)
	}
	return doc
}

// TestPreStartWritesTheMemoryProviderAndItsConfig is the whole wiring in one
// pass: the node the harness activates, and the file that node reads to reach the
// server.
func TestPreStartWritesTheMemoryProviderAndItsConfig(t *testing.T) {
	dir := t.TempDir()
	c := memoryHermes()

	result, err := c.PreStart(context.Background(), memoryState(dir, hindsightBinding()))
	require.NoError(t, err)
	require.True(t, result.RestartNeeded, "the first write must recreate the container so the gateway reads it")

	doc := readConfigDoc(t, dir)
	memory, ok := doc[memoryKey].(map[string]any)
	require.True(t, ok, "the memory section must be written")
	assert.Equal(t, "hindsight", memory[memoryProviderKey])

	cfg := readMemoryConfig(t, dir)
	assert.Equal(t, "local_external", cfg["mode"])
	assert.Equal(t, "http://localhost:9999", cfg["api_url"],
		"the plugin dials the host loopback: the harness shares the host network namespace, so a container name will not resolve")
	assert.Equal(t, "memory-bearer", cfg["api_key"])
	assert.Equal(t, memoryBankID, cfg["bank_id"])
	assert.Equal(t, memoryProviderMode, cfg["memory_mode"])
}

// TestPreStartMemoryIdempotent is invariant 2 for the second managed file: the
// address and bank have not moved, so the pass must be a read-only diff.
func TestPreStartMemoryIdempotent(t *testing.T) {
	dir := t.TempDir()
	c := memoryHermes()
	state := memoryState(dir, hindsightBinding())

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a steady-state pass must touch nothing")
}

// TestPreStartMemoryKeepsOperatorChoices covers the adopt-once rule for the two
// keys the operator owns. A bank and a mode that were typed by hand are statements
// about where the agent's memory lives, and Bloud rewrites only the connection.
func TestPreStartMemoryKeepsOperatorChoices(t *testing.T) {
	dir := t.TempDir()
	c := memoryHermes()
	path := filepath.Join(dir, "data", "hindsight", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"bank_id":"family","memory_mode":"tools","recall_budget":"high"}`), 0o644))

	_, err := c.PreStart(context.Background(), memoryState(dir, hindsightBinding()))
	require.NoError(t, err)

	cfg := readMemoryConfig(t, dir)
	assert.Equal(t, "family", cfg["bank_id"], "the operator's bank survives")
	assert.Equal(t, "tools", cfg["memory_mode"], "the operator's mode survives")
	assert.Equal(t, "high", cfg["recall_budget"], "a key Bloud does not own survives untouched")
	assert.Equal(t, "http://localhost:9999", cfg["api_url"], "the connection is Bloud's and is written")
}

// TestPreStartMemoryProviderAdoptOnce covers `memory.provider`, which is a
// statement the operator makes and Bloud does not overwrite.
func TestPreStartMemoryProviderAdoptOnce(t *testing.T) {
	cases := map[string]struct {
		existing  string
		want      string
		wantMoved bool
	}{
		"unset":              {existing: "", want: "hindsight", wantMoved: true},
		"operator chose one": {existing: "memory:\n    provider: mem0\n", want: "mem0"},
		"already ours":       {existing: "memory:\n    provider: hindsight\n", want: "hindsight"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.existing != "" {
				writeExistingConfig(t, dir, tc.existing)
			}
			c := memoryHermes()
			_, err := c.PreStart(context.Background(), memoryState(dir, hindsightBinding()))
			require.NoError(t, err)

			doc := readConfigDoc(t, dir)
			memory, _ := doc[memoryKey].(map[string]any)
			require.NotNil(t, memory)
			assert.Equal(t, tc.want, memory[memoryProviderKey])
		})
	}
}

// TestPreStartStripsOnlyBloudMemoryProviders is the removal half of adopt-once.
// An uninstalled provider still resolves, with Installed false, so Bloud knows the
// name is its to remove. A name it does not declare belongs to the operator.
func TestPreStartStripsOnlyBloudMemoryProviders(t *testing.T) {
	cases := map[string]struct {
		existing   string
		binding    bindings.MemoryBinding
		wantGone   bool
		wantString string
	}{
		"provider uninstalled": {
			existing: "memory:\n    provider: hindsight\n",
			binding:  func() bindings.MemoryBinding { b := hindsightBinding(); b.Installed = false; return b }(),
			wantGone: true,
		},
		"token unpublished": {
			existing: "memory:\n    provider: hindsight\n",
			binding:  func() bindings.MemoryBinding { b := hindsightBinding(); b.Token = ""; return b }(),
			wantGone: true,
		},
		"operator's provider": {
			existing:   "memory:\n    provider: mem0\n",
			binding:    hindsightBinding(),
			wantString: "mem0",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeExistingConfig(t, dir, tc.existing)
			c := memoryHermes()
			_, err := c.PreStart(context.Background(), memoryState(dir, tc.binding))
			require.NoError(t, err)

			doc := readConfigDoc(t, dir)
			memory, _ := doc[memoryKey].(map[string]any)
			if tc.wantGone {
				assert.Nil(t, memory, "the section goes with its last key")
				return
			}
			require.NotNil(t, memory)
			assert.Equal(t, tc.wantString, memory[memoryProviderKey])
		})
	}
}

// TestPreStartMemoryIgnoresAnIncompleteBinding covers the "not ready" states. Each
// of these would configure something that cannot work: a plugin with no pin, or a
// server address that is only a container name.
func TestPreStartMemoryIgnoresAnIncompleteBinding(t *testing.T) {
	cases := map[string]func(bindings.MemoryBinding) bindings.MemoryBinding{
		"no plugin commit": func(b bindings.MemoryBinding) bindings.MemoryBinding { b.PluginCommit = ""; return b },
		"no token":         func(b bindings.MemoryBinding) bindings.MemoryBinding { b.Token = ""; return b },
		"not installed":    func(b bindings.MemoryBinding) bindings.MemoryBinding { b.Installed = false; return b },
		"no address": func(b bindings.MemoryBinding) bindings.MemoryBinding {
			b.LocalURL, b.BaseURL = "", ""
			return b
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c := memoryHermes()
			_, err := c.PreStart(context.Background(), memoryState(dir, mutate(hindsightBinding())))
			require.NoError(t, err)

			doc, written := readConfigDocIfPresent(t, dir)
			if written {
				assert.Nil(t, doc[memoryKey], "no usable provider means no provider is written")
			}
			assert.NoFileExists(t, filepath.Join(dir, "data", "hindsight", "config.json"),
				"and no config file points at a server it cannot reach")
		})
	}
}

// TestPreStartMemoryFallsBackToTheCatalogID covers a provider that does not name
// its plugin: the app id is the namespace, the same rule the MCP wiring uses, and
// writing an empty provider would be a section Hermes reads as a choice of
// nothing.
func TestPreStartMemoryFallsBackToTheCatalogID(t *testing.T) {
	dir := t.TempDir()
	b := hindsightBinding()
	b.PluginName = ""

	c := memoryHermes()
	_, err := c.PreStart(context.Background(), memoryState(dir, b))
	require.NoError(t, err)

	memory, _ := readConfigDoc(t, dir)[memoryKey].(map[string]any)
	require.NotNil(t, memory)
	assert.Equal(t, "hindsight", memory[memoryProviderKey])
}

// TestPreStartMemoryDegradesOnABadPluginPath keeps a declared path from walking
// out of the app's data tree. The value arrives from an offer, and it is joined
// onto a filesystem path, so it is checked rather than trusted.
func TestPreStartMemoryDegradesOnABadPluginPath(t *testing.T) {
	for _, bad := range []string{"", "/etc/bloud/config.json", "../hindsight/config.json", "hindsight/../../config.json", "hindsight//config.json"} {
		t.Run(bad, func(t *testing.T) {
			dir := t.TempDir()
			c := memoryHermes()
			b := hindsightBinding()
			b.PluginConfigPath = bad

			_, err := c.PreStart(context.Background(), memoryState(dir, b))
			require.Error(t, err, "a bad declared path must be reported, not repaired silently")
			assert.NoFileExists(t, filepath.Join(dir, "data", "config.json"))
			assert.NoFileExists(t, filepath.Join(filepath.Dir(dir), "hindsight", "config.json"))
		})
	}
}

// TestPreStartMemoryOnlyWritesTheProviderSection keeps the managed edit inside
// its own section. The address of the provider changing must not disturb SSO, the
// inference provider, or a hand-written key.
func TestPreStartMemoryOnlyWritesTheProviderSection(t *testing.T) {
	dir := t.TempDir()
	writeExistingConfig(t, dir, "model:\n    provider: custom:bloud\noperator_key: keep-me\n")
	c := memoryHermes()

	_, err := c.PreStart(context.Background(), memoryState(dir, hindsightBinding()))
	require.NoError(t, err)

	doc := readConfigDoc(t, dir)
	assert.Equal(t, "keep-me", doc["operator_key"])
	model, _ := doc["model"].(map[string]any)
	require.NotNil(t, model)
	assert.Equal(t, "custom:bloud", model["provider"])
}

// TestStartMemoryProviderInstallsAndRestarts covers the pass that has work to do:
// the plugin is not on disk, so it is installed at the pinned commit and the
// container is restarted to load it.
func TestStartMemoryProviderInstallsAndRestarts(t *testing.T) {
	dir := t.TempDir()
	var ran [][]string
	restarts := 0
	c := memoryHermes()
	c.exec = func(_ context.Context, name string, _ map[string]string, cmd []string) ([]byte, error) {
		require.Equal(t, nodeName, name)
		ran = append(ran, cmd)
		writePluginRecord(t, dir, "hindsight", hindsightBinding().PluginCommit)
		return nil, nil
	}
	c.restartContainer = func(_ context.Context, name string) error {
		require.Equal(t, nodeName, name)
		restarts++
		return nil
	}

	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(dir, hindsightBinding())))
	require.Len(t, ran, 1)
	assert.Equal(t, []string{
		"hermes", "plugins", "install", "hindsight",
		"--ref", hindsightBinding().PluginCommit, "--enable",
	}, ran[0])
	assert.Equal(t, 1, restarts, "the gateway only reads the plugin at startup")
}

// TestStartMemoryProviderLeavesAPinnedInstallAlone is the steady state. It is the
// property that keeps a working agent from being restarted every minute: the
// record on disk matches the declaration, so nothing runs.
func TestStartMemoryProviderLeavesAPinnedInstallAlone(t *testing.T) {
	dir := t.TempDir()
	writePluginRecord(t, dir, "hindsight", hindsightBinding().PluginCommit)

	executed := false
	restarts := 0
	c := memoryHermes()
	c.exec = func(context.Context, string, map[string]string, []string) ([]byte, error) {
		executed = true
		return nil, nil
	}
	c.restartContainer = func(context.Context, string) error { restarts++; return nil }

	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(dir, hindsightBinding())))
	assert.False(t, executed, "a plugin already at the pinned commit is not reinstalled")
	assert.Zero(t, restarts)
}

// TestStartMemoryProviderMovesThePin covers a bumped commit. `hermes plugins
// install` refuses an existing plugin, so the only way to move the pin is the
// reinstall form, and it is used only when the commit actually differs.
func TestStartMemoryProviderMovesThePin(t *testing.T) {
	dir := t.TempDir()
	writePluginRecord(t, dir, "hindsight", "0000000000000000000000000000000000000000")

	var ran []string
	c := memoryHermes()
	c.exec = func(_ context.Context, _ string, _ map[string]string, cmd []string) ([]byte, error) {
		ran = cmd
		writePluginRecord(t, dir, "hindsight", hindsightBinding().PluginCommit)
		return nil, nil
	}
	c.restartContainer = func(context.Context, string) error { return nil }

	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(dir, hindsightBinding())))
	assert.Contains(t, ran, "--force", "an existing plugin only moves with the reinstall form")
	ref := indexOf(t, ran, "--ref")
	assert.Equal(t, hindsightBinding().PluginCommit, ran[ref+1],
		"the pinned commit is what the command asks for")
}

// TestStartMemoryProviderReplacesAPluginWithNoRecord covers the plugin that is on
// disk but did not come from the catalog: a hand-made install, or one from a
// branch. Bloud cannot tell which commit it is at, and the declaration is what this
// repository reviewed, so it is replaced. Without this the install command would
// refuse the existing plugin on every pass and log a warning forever while the
// reviewed pin never landed.
func TestStartMemoryProviderReplacesAPluginWithNoRecord(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data", pluginsSubdir, "hindsight"), 0o755))

	var ran []string
	c := memoryHermes()
	c.exec = func(_ context.Context, _ string, _ map[string]string, cmd []string) ([]byte, error) {
		ran = cmd
		writePluginRecord(t, dir, "hindsight", hindsightBinding().PluginCommit)
		return nil, nil
	}
	c.restartContainer = func(context.Context, string) error { return nil }

	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(dir, hindsightBinding())))
	assert.Contains(t, ran, "--force")
}

// TestStartMemoryProviderReportsANetworkFailureWithoutFailingTheNode is the
// reason this step is best-effort. Memory is an enhancement: a clone that fails
// on a slow network must leave a working agent alone and be retried next pass,
// which is what an ERROR node would prevent.
func TestStartMemoryProviderReportsANetworkFailureWithoutFailingTheNode(t *testing.T) {
	dir := t.TempDir()
	c := memoryHermes()
	c.exec = func(context.Context, string, map[string]string, []string) ([]byte, error) {
		return []byte("fatal: unable to access 'https://github.com/': Could not resolve host"), assert.AnError
	}
	c.restartContainer = func(context.Context, string) error {
		t.Fatal("a failed install must not restart the container")
		return nil
	}

	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(dir, hindsightBinding())))
}

// TestStartMemoryProviderWithoutAnExecIsNotAnError covers the CLI and test
// contexts, where no container can be reached. "Cannot apply now" is not
// "applied", and it is not a failure either.
func TestStartMemoryProviderWithoutAnExecIsNotAnError(t *testing.T) {
	c := memoryHermes()
	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(t.TempDir(), hindsightBinding())))
}

// TestStartMemoryProviderWithoutAProviderIsANoOp covers the ordinary install of an
// agent with no memory server: nothing to install, nothing to restart.
func TestStartMemoryProviderWithoutAProviderIsANoOp(t *testing.T) {
	c := memoryHermes()
	c.exec = func(context.Context, string, map[string]string, []string) ([]byte, error) {
		t.Fatal("no provider means no install")
		return nil, nil
	}
	require.NoError(t, c.startMemoryProvider(context.Background(), memoryState(t.TempDir())))
}

// TestPruneRetiredMCPServers is the upgrade path. Hindsight's namespace stays in
// an operator's config after the wiring is gone, and leaving it would point the
// model at a route the gate no longer serves.
func TestPruneRetiredMCPServers(t *testing.T) {
	dir := t.TempDir()
	writeExistingConfig(t, dir, "mcp_servers:\n    hindsight:\n        url: http://localhost:9999/mcp\n    affine:\n        url: http://localhost:3010/mcp\n")
	c := memoryHermes()

	_, err := c.PreStart(context.Background(), memoryState(dir, hindsightBinding()))
	require.NoError(t, err)

	doc := readConfigDoc(t, dir)
	servers, ok := doc[mcpServersKey].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, servers, "hindsight", "the retired namespace is removed")
	assert.Contains(t, servers, "affine", "an unrelated namespace is untouched")
}

// TestPruneRetiredMCPServersKeepsALiveNamespace guards the other direction: if a
// provider offers that name again, the prune must not delete a wired entry.
func TestPruneRetiredMCPServersKeepsALiveNamespace(t *testing.T) {
	dir := t.TempDir()
	writeExistingConfig(t, dir, "mcp_servers:\n    hindsight:\n        url: http://localhost:9999/mcp\n")
	c := memoryHermes()

	live := mcpState(dir, bindings.MCPBinding{
		ProviderRef: bindings.ProviderRef{App: "hindsight", Installed: true, LocalURL: "http://localhost:9999"},
		ServerName:  "hindsight",
		Token:       "token",
		Path:        "/mcp",
	})
	live.Integrations.Memories = memoryState(dir, hindsightBinding()).Integrations.Memories

	_, err := c.PreStart(context.Background(), live)
	require.NoError(t, err)

	servers, _ := readConfigDoc(t, dir)[mcpServersKey].(map[string]any)
	require.NotNil(t, servers)
	assert.Contains(t, servers, "hindsight")
}

// TestRetiredMCPServersGuardAgainstNothing keeps the table honest: every entry
// must name a namespace nothing offers any more. An entry whose provider came back
// would delete a live entry on every pass.
func TestRetiredMCPServersGuardAgainstNothing(t *testing.T) {
	require.NotEmpty(t, retiredMCPServers)
	raw, err := os.ReadFile(filepath.Join("..", "hindsight", "metadata.yaml"))
	require.NoError(t, err)
	// The provider that used to serve these names must no longer declare the
	// contract. Reading the file as text keeps this test free of the catalog
	// loader, which lives in the host-agent module.
	for name, why := range retiredMCPServers {
		assert.NotEmpty(t, why, "the table carries the reason for each entry")
		assert.NotContains(t, string(raw), "mcp:",
			"%s is retired (%s) but its provider declares mcp again", name, why)
	}
}

// TestPluginConfigRelPathRejectsTraversal covers the check directly, including the
// shapes the PreStart test cannot express as a file path on every platform.
func TestPluginConfigRelPathRejectsTraversal(t *testing.T) {
	b := hindsightBinding()
	for _, good := range []string{"hindsight/config.json", "plugins/hindsight/config.json"} {
		b.PluginConfigPath = good
		rel, err := pluginConfigRelPath(b)
		require.NoError(t, err, good)
		assert.Equal(t, good, strings.TrimSpace(rel))
	}
	for _, bad := range []string{"", " ", "/abs/config.json", "../x.json", "a/../b.json", "a//b.json", "./a.json", "a/./b.json"} {
		b.PluginConfigPath = bad
		_, err := pluginConfigRelPath(b)
		require.Errorf(t, err, "must reject %q", bad)
	}
}

// writePluginRecord seeds the record Hermes writes inside an installed plugin.
func writePluginRecord(t *testing.T, dir, name, commit string) {
	t.Helper()
	path := filepath.Join(dir, "data", pluginsSubdir, name, memoryPluginRecord)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"sha":"`+commit+`"}`), 0o644))
}

// readConfigDocIfPresent reads config.yaml, reporting whether a pass wrote one at
// all. A pass with nothing to manage leaves the file absent rather than writing an
// empty document.
func readConfigDocIfPresent(t *testing.T, dir string) (map[string]any, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	if os.IsNotExist(err) {
		return nil, false
	}
	require.NoError(t, err)
	doc := map[string]any{}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	return doc, true
}

// indexOf returns the position of a flag in an argument list.
func indexOf(t *testing.T, args []string, flag string) int {
	t.Helper()
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	t.Fatalf("%q not in %v", flag, args)
	return -1
}
