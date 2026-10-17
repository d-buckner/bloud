// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

// Long-term memory is the one integration where the harness, not the model, is
// the client. An MCP tool namespace leaves the remembering to the model: it calls
// `retain` when it thinks to, and `recall` when it thinks to look. A memory
// provider inverts that. Hermes loads the provider into the agent, captures every
// turn through it, and injects what it has learned before the next answer, with no
// tool call in between.
//
// That is a plugin, and Hermes owns plugins: this file installs the provider's
// plugin, pins it, and gives it the address and bearer of the server Bloud runs.
// The provider declares all four facts it needs (see the `memory` contract in
// internal/catalog/contracts.go), so a second memory provider arrives as metadata
// rather than as code here.
//
// Two rules shape everything below.
//
// The operator owns the choice. `memory.provider` selects the harness's one memory
// provider, and Hermes reads it from its own config file. Bloud writes that key
// only when the operator has not, and removes it only when it names a provider
// Bloud is responsible for. An operator who pointed Hermes at a different memory
// server keeps it, and Bloud stops writing this one's config on the next pass.
//
// The install is pinned, like every container image in this catalog. `hermes
// plugins install` follows the vendor's catalog, whose pin moves without this
// repository reviewing it, so Bloud passes the commit the provider declares in its
// metadata instead. See INTEGRATION.md for what the pin costs.

const (
	// memoryKey is the Hermes config section holding the memory provider choice.
	memoryKey = "memory"
	// memoryProviderKey is the key within it. Its value is the plugin name the
	// provider declares, which is what `hermes memory setup` writes too.
	memoryProviderKey = "provider"

	// memoryBankID and memoryProviderMode are the values Bloud adopts when the
	// plugin's config carries neither. They match the plugin's own defaults, and
	// they are written down for the reason every other default is: an upstream
	// that changed one would move where every fact the agent learned is stored,
	// with nothing in this repository showing it.
	//
	// The bank is per-harness on purpose. A bank is a memory, and the one the
	// agent keeps for itself should not be the one an operator's other tools
	// write to.
	memoryBankID       = "hermes"
	memoryProviderMode = "hybrid"

	// memoryPluginRecord is the file Hermes writes inside an installed plugin,
	// recording the catalog entry and commit it came from. It is how this
	// configurator tells the plugin it pinned from the plugin it pinned last
	// time, without keeping state of its own: the bytes on disk are the record.
	memoryPluginRecord = ".hermes-catalog.json"

	// pluginsSubdir is where Hermes installs plugin code under $HERMES_HOME. It
	// is on the app's data mount, so an installed plugin outlives the container
	// that installed it.
	pluginsSubdir = "plugins"
)

// applyMemoryProvider writes the harness's memory provider choice, or removes the
// one Bloud wrote.
//
// The rule is adopt-once, the same one `applyToolSearch` and `adoptDefaultModel`
// follow: an operator who set `memory.provider` keeps that value, whichever
// provider it names. Bloud writes only into an unset key.
//
// Removal is narrower than writing, and it has to be: the value Bloud wrote is a
// name that arrives from the provider's metadata, so there is no constant to
// compare against. What Bloud may remove is a value naming a provider it is
// responsible for, which is exactly the set of memory providers the catalog
// declares. `memory.provider: mem0` is the operator's and survives every pass.
func applyMemoryProvider(doc map[string]any, state *configurator.AppState) {
	declared := declaredMemoryPlugins(state)
	memory, ok := writableSection(doc, memoryKey)
	if !ok {
		return
	}

	b, usable := memoryBinding(state)
	if !usable {
		stripMemoryProvider(doc, declared)
		return
	}
	if existing, ok := memory[memoryProviderKey].(string); ok && strings.TrimSpace(existing) != "" {
		return
	}
	// Attach the section only on the write path, so a pass that decides to do
	// nothing cannot leave an empty `memory: {}` behind and report a change.
	doc[memoryKey] = memory
	memory[memoryProviderKey] = memoryPluginName(b)
}

// stripMemoryProvider removes the provider choice when it names one Bloud
// declares, and leaves anything else alone. Empty sections go with it, so a
// config with no memory provider has no `memory:` key rather than one Hermes reads
// as a malformed section.
func stripMemoryProvider(doc map[string]any, declared map[string]bool) {
	memory, ok := doc[memoryKey].(map[string]any)
	if !ok {
		return
	}
	value, ok := memory[memoryProviderKey].(string)
	if !ok || !declared[strings.TrimSpace(value)] {
		return
	}
	delete(memory, memoryProviderKey)
	if len(memory) == 0 {
		delete(doc, memoryKey)
	}
}

// declaredMemoryPlugins is the set of plugin names Bloud is responsible for: every
// memory provider this app declares compatible, installed or not. Reading the
// declaration rather than the installation is what lets an uninstall hand the
// choice back: the binding stays in the set with Installed false, so the value is
// still Bloud's to remove.
func declaredMemoryPlugins(state *configurator.AppState) map[string]bool {
	out := map[string]bool{}
	for _, b := range memoryBindings(state) {
		if name := memoryPluginName(b); name != "" {
			out[name] = true
		}
	}
	return out
}

// memoryBindings returns every resolved memory binding, including providers that
// are declared but not installed.
func memoryBindings(state *configurator.AppState) []bindings.MemoryBinding {
	if state == nil {
		return nil
	}
	return state.Integrations.Memories
}

// memoryPluginName is the name Hermes installs and activates for one binding.
// The provider declares it; the catalog id is the fallback, matching the plugin
// key rule the MCP wiring uses.
func memoryPluginName(b bindings.MemoryBinding) string {
	if name := strings.TrimSpace(b.PluginName); name != "" {
		return name
	}
	return strings.TrimSpace(b.App)
}

// memoryBinding returns the one provider Bloud can configure Hermes with, and
// reports whether there is one.
//
// Hermes activates exactly one memory provider, so this picks rather than
// collects. The order is the catalog's declaration order, so the choice is stable
// across passes. A provider is usable only when it is installed, has published
// the bearer its API checks, and declares where its plugin comes from: a binding
// missing any of those would configure a plugin that cannot reach the server, or
// leave a server running that nothing reads.
func memoryBinding(state *configurator.AppState) (bindings.MemoryBinding, bool) {
	for _, b := range memoryBindings(state) {
		if !b.Installed || !b.Token.Published() {
			continue
		}
		if memoryPluginName(b) == "" || strings.TrimSpace(b.PluginCommit) == "" {
			continue
		}
		if memoryAPIOrigin(b) == "" {
			continue
		}
		return b, true
	}
	return bindings.MemoryBinding{}, false
}

// memoryAPIOrigin is the address the plugin dials, as the configurator would.
//
// LocalURL is the address, not BaseURL, for the reason the MCP wiring gives:
// BaseURL is `http://<container>:<port>`, which podman's network-scoped DNS serves
// only to containers on that network, and Hermes runs in the host network namespace
// for its own OIDC reasons. The host loopback is what both agree on.
func memoryAPIOrigin(b bindings.MemoryBinding) string {
	base := strings.TrimSpace(b.LocalURL)
	if base == "" {
		base = strings.TrimSpace(b.BaseURL)
	}
	return strings.TrimSuffix(base, "/")
}

// writeMemoryConfig writes the plugin's own config file, which is how the server's
// address and bearer reach it. The plugin reads process environment only when this
// file is absent, and the file is the better carrier of a credential: it is the
// plugin's own, so Bloud does not write Hermes' `.env`, which Hermes owns and mints
// its own agent key into.
//
// The connection keys are Bloud's and are rewritten on every pass: they name the
// server Bloud runs, and a rotated bearer or a moved address has to land. The
// shaping keys are adopt-once, because they are the operator's: an operator who
// pointed the agent at another bank keeps it.
func (c *Configurator) writeMemoryConfig(state *configurator.AppState, b bindings.MemoryBinding) (bool, error) {
	if state == nil || state.DataPath == "" {
		return false, nil
	}
	rel, err := pluginConfigRelPath(b)
	if err != nil {
		return false, err
	}
	path := filepath.Join(state.DataPath, "data", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("creating the memory plugin config directory: %w", err)
	}

	doc := map[string]any{}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		// A corrupt file is the operator's or a previous version's; starting from
		// what parses keeps the keys Bloud does not own instead of discarding them.
		_ = json.Unmarshal(raw, &doc)
	} else if !os.IsNotExist(readErr) {
		return false, fmt.Errorf("reading %s: %w", path, readErr)
	}

	doc["mode"] = "local_external"
	doc["api_url"] = memoryAPIOrigin(b)
	doc["api_key"] = b.Token.String()
	if _, ok := doc["bank_id"]; !ok {
		doc["bank_id"] = memoryBankID
	}
	if _, ok := doc["memory_mode"]; !ok {
		doc["memory_mode"] = memoryProviderMode
	}

	want, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, fmt.Errorf("serializing %s: %w", path, err)
	}
	want = append(want, '\n')
	return managedfile.Write(path, want, managedfile.ModeSharedConfig)
}

// pluginConfigRelPath resolves the provider-declared config location.
//
// The value is a relative path under $HERMES_HOME, and it is checked here rather
// than trusted: a declared `../../etc` would put Bloud's write outside the app's
// own data tree. A path that fails the check is reported, not repaired, because
// the metadata is wrong and silently substituting another location would hide it.
func pluginConfigRelPath(b bindings.MemoryBinding) (string, error) {
	rel := strings.TrimSpace(b.PluginConfigPath)
	switch {
	case rel == "":
		return "", fmt.Errorf("memory provider %q declares no pluginConfigPath", b.App)
	case strings.HasPrefix(rel, "/"), filepath.IsAbs(rel):
		return "", fmt.Errorf("memory provider %q declares pluginConfigPath %q, which is not relative to the harness home", b.App, rel)
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("memory provider %q declares pluginConfigPath %q, which walks outside the harness home", b.App, rel)
		}
	}
	return rel, nil
}

// ensureMemoryPlugin makes the installed plugin match the pinned one, and reports
// whether it had to install anything.
//
// The check is the record Hermes writes inside the plugin directory, not the
// directory's existence: a bump to the commit a provider declares has to land, and
// the only way to see that it has not is to read the commit that is there. A
// directory with no record is an install from somewhere else, and the declaration
// wins: Bloud reviews the pin, and the log line says a replacement happened.
//
// Failure is reported to the caller, not raised as a phase failure. Memory is an
// enhancement, and a DNS hiccup while cloning should not park a working agent in
// ERROR: the next pass retries, and the log says what the agent is missing.
func (c *Configurator) ensureMemoryPlugin(
	ctx context.Context,
	state *configurator.AppState,
	b bindings.MemoryBinding,
) (bool, error) {
	if c.exec == nil || state == nil || state.DataPath == "" {
		return false, nil
	}
	name := memoryPluginName(b)
	commit := strings.TrimSpace(b.PluginCommit)
	pluginDir := filepath.Join(state.DataPath, "data", pluginsSubdir, name)
	record, err := readPluginRecord(pluginDir)
	if err != nil {
		return false, err
	}
	if record == commit {
		return false, nil
	}

	args := []string{"hermes", "plugins", "install", name, "--ref", commit, "--enable"}
	if pluginDirExists(pluginDir) {
		// The plugin is there at another commit, or there with no record of where
		// it came from. `install` refuses an existing plugin, so the only way to
		// make the installed plugin the declared one is the reinstall form. An
		// unknown record counts: a plugin from anywhere else was not reviewed with
		// this image, and the declaration is what Bloud is responsible for.
		args = append(args, "--force")
		c.logger.Info("replacing the Hermes memory plugin to match the pinned commit",
			"plugin", name, "installed", installedCommitLabel(record), "pinned", commit)
	}
	if _, execErr := c.exec(ctx, nodeName, nil, args); execErr != nil {
		return false, fmt.Errorf("installing the %s plugin at %s: %w", name, commit, execErr)
	}

	// Confirm against the record rather than the exit status: the command reports
	// success for a plugin that installed and failed to enable, and the only
	// trustworthy answer is the commit written on disk.
	installed, err := readPluginRecord(pluginDir)
	if err != nil {
		return false, err
	}
	if installed != commit {
		return false, fmt.Errorf("the %s plugin is at %q after installing %s", name, installed, commit)
	}
	return true, nil
}

// pluginDirExists reports whether a plugin directory is there, whatever commit it
// holds. It is the question that decides between an install and a reinstall.
func pluginDirExists(dir string) bool {
	_, err := os.Stat(dir)
	return err == nil
}

// installedCommitLabel names what the record said for the log line. An empty record
// on an existing directory is a plugin from outside the catalog, and saying so is
// clearer than printing an empty string beside a commit.
func installedCommitLabel(record string) string {
	if record == "" {
		return "unknown"
	}
	return record
}

// readPluginRecord returns the commit an installed plugin came from, or the empty
// string when no plugin is installed. A record that is present but unreadable reads
// as "not the pinned commit", which is what makes it get rewritten.
func readPluginRecord(pluginDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(pluginDir, memoryPluginRecord))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		if os.IsPermission(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading the installed plugin record: %w", err)
	}
	var record struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", nil
	}
	return strings.TrimSpace(record.SHA), nil
}
