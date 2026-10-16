// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bloud wires every installed MCP provider into Hermes, and the schemas are
// large: 106 tools from affine-mcp, 77 from ha-mcp, 27 from dav-mcp. Tool
// Search is what keeps those out of the model's context on every turn, so the
// pass that writes a namespace also pins the disclosure layer on.
func TestPreStartPinsToolSearchWhenMCPServersExist(t *testing.T) {
	dir := t.TempDir()

	if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	doc := readConfigDoc(t, dir)
	if got := nested(t, doc, toolsKey, toolSearchKey)[toolSearchEnabledKey]; got != toolSearchEnabledValue {
		t.Errorf("%s.%s = %v, want %q", toolsKey, toolSearchKey, got, toolSearchEnabledValue)
	}
}

// With no MCP namespace there is nothing to defer, so Bloud states no policy.
// The section must be absent rather than an empty `tools: {}`, because an empty
// map is a document change and a change is a container restart.
func TestPreStartWritesNoToolSearchWithoutMCPServers(t *testing.T) {
	dir := t.TempDir()

	if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir)); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading written config: %v", err)
	}
	if strings.Contains(string(raw), toolSearchKey) {
		t.Errorf("no MCP namespace means no tool-search policy, got:\n%s", raw)
	}
}

// The provider can be installed but not yet usable (no published credential).
// That writes no namespace, so it must not write the pin either: the two
// managed sections describe the same agent and cannot disagree.
func TestPreStartNoToolSearchPinForUnusableProvider(t *testing.T) {
	dir := t.TempDir()
	b := affineBinding()
	b.Token = ""

	if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir, b)); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	if err == nil && strings.Contains(string(raw), toolsKey) {
		t.Errorf("a binding that registers no namespace must write no tool-search policy, got:\n%s", raw)
	}
}

// Uninstalling the last provider removes the pin with it, so the config does not
// keep asserting a policy about namespaces that are gone.
func TestPreStartRemovesToolSearchWhenLastProviderGoes(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart (install): %v", err)
	}
	if _, ok := readConfigDoc(t, dir)[toolsKey]; !ok {
		t.Fatal("precondition: the pin was written")
	}

	gone := affineBinding()
	gone.Installed = false
	if _, err := c.PreStart(context.Background(), mcpState(dir, gone)); err != nil {
		t.Fatalf("PreStart (uninstall): %v", err)
	}

	if _, ok := readConfigDoc(t, dir)[toolsKey]; ok {
		t.Errorf("%s must be gone once its only MCP provider is uninstalled", toolsKey)
	}
}

// An operator who set the switch keeps their value, including `off`. Bloud adds
// the namespaces, but the operator owns how many of them the model pays for.
func TestPreStartPreservesOperatorToolSearchPolicy(t *testing.T) {
	for _, policy := range []string{"off", "auto", "on"} {
		t.Run(policy, func(t *testing.T) {
			dir := t.TempDir()
			writeExistingConfig(t, dir, "tools:\n  tool_search:\n    enabled: "+policy+"\n")

			if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
				t.Fatalf("PreStart: %v", err)
			}

			doc := readConfigDoc(t, dir)
			if got := nested(t, doc, toolsKey, toolSearchKey)[toolSearchEnabledKey]; got != policy {
				t.Errorf("enabled = %v, want the operator's %q", got, policy)
			}
			if !hasNamespace(doc, "affine") {
				t.Error("the MCP namespace must still be written; the policy is about disclosure, not wiring")
			}
		})
	}
}

// The same rule on removal: an operator value survives the loss of the last
// provider, so uninstalling a provider cannot silently re-enable a policy the
// operator had turned off.
func TestPreStartKeepsOperatorToolSearchOnRemoval(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()
	writeExistingConfig(t, dir, "tools:\n  tool_search:\n    enabled: off\n")

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart (install): %v", err)
	}
	gone := affineBinding()
	gone.Installed = false
	if _, err := c.PreStart(context.Background(), mcpState(dir, gone)); err != nil {
		t.Fatalf("PreStart (uninstall): %v", err)
	}

	if got := nested(t, readConfigDoc(t, dir), toolsKey, toolSearchKey)[toolSearchEnabledKey]; got != "off" {
		t.Errorf("enabled = %v, want the operator's off", got)
	}
}

// `tools: {tool_search: true}` is the legacy boolean shape Hermes documents. It
// is an operator statement, so Bloud leaves the shape alone rather than
// rewriting it into a map.
func TestPreStartKeepsLegacyBooleanToolSearchShape(t *testing.T) {
	dir := t.TempDir()
	writeExistingConfig(t, dir, "tools:\n  tool_search: true\n")

	if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	doc := readConfigDoc(t, dir)
	if got := doc[toolsKey].(map[string]any)[toolSearchKey]; got != true {
		t.Errorf("tool_search = %v, want the operator's boolean left intact", got)
	}
}

// A managed key claims one leaf. The operator's other tool-search settings and
// the sibling `tools.connectors` section survive, because Hermes deep-merges
// config.yaml over its defaults and a replaced section would drop them.
func TestPreStartKeepsOperatorToolSettings(t *testing.T) {
	dir := t.TempDir()
	writeExistingConfig(t, dir, "tools:\n  connectors:\n    enabled: false\n  tool_search:\n    listing_max_tokens: 900\n")

	if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	tools := readConfigDoc(t, dir)[toolsKey].(map[string]any)
	if got := nested(t, tools, toolSearchKey)["listing_max_tokens"]; got != 900 {
		t.Errorf("listing_max_tokens = %v, want the operator's 900", got)
	}
	if got := nested(t, tools, "connectors")["enabled"]; got != false {
		t.Errorf("connectors.enabled = %v, want the operator's false", got)
	}
	if got := nested(t, tools, toolSearchKey)[toolSearchEnabledKey]; got != toolSearchEnabledValue {
		t.Errorf("enabled = %v, want the Bloud pin", got)
	}
}

// Steady state: the pin must not report a change on the next pass, or the
// periodic resync recreates the container every minute (invariant 2).
func TestPreStartToolSearchIdempotent(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	changed, err := c.PreStart(context.Background(), mcpState(dir, affineBinding()))
	if err != nil {
		t.Fatalf("PreStart (second pass): %v", err)
	}
	if changed.RestartNeeded {
		t.Error("an unchanged pin must not report a change")
	}
}

// Removing the pin leaves any other key in the section, and deletes the section
// only when nothing is left in it.
func TestStripToolSearchKeepsSiblings(t *testing.T) {
	doc := map[string]any{
		toolsKey: map[string]any{
			toolSearchKey: map[string]any{
				toolSearchEnabledKey: toolSearchEnabledValue,
				"listing":            "off",
			},
		},
	}
	stripToolSearch(doc)

	search := nested(t, doc, toolsKey, toolSearchKey)
	if _, ok := search[toolSearchEnabledKey]; ok {
		t.Error("the Bloud pin should have been removed")
	}
	if search["listing"] != "off" {
		t.Error("the operator's own tool-search setting must remain")
	}
	if _, ok := doc[toolsKey]; !ok {
		t.Error("the section must stay while an operator key remains in it")
	}

	delete(search, "listing")
	stripToolSearch(doc)
	if _, ok := doc[toolsKey]; !ok {
		t.Error("a section holding only operator keys must stay even once the pin is gone")
	}
}

// When the pin is the only key, the whole section goes with it, so a config with
// no tool policy has no empty `tools:` key.
func TestStripToolSearchDeletesEmptySections(t *testing.T) {
	doc := map[string]any{
		toolsKey: map[string]any{
			toolSearchKey: map[string]any{toolSearchEnabledKey: toolSearchEnabledValue},
		},
	}
	stripToolSearch(doc)
	if _, ok := doc[toolsKey]; ok {
		t.Errorf("an emptied tools section should be deleted, not left as an empty map: %v", doc)
	}
}

// The log line reports the merged state, so an operator value reads as the
// operator's, not as the pin Bloud wanted.
func TestToolSearchPolicy(t *testing.T) {
	for name, doc := range map[string]map[string]any{
		"unset":      {},
		"tools-gone": {toolsKey: "not-a-map"},
		"on":         {toolsKey: map[string]any{toolSearchKey: map[string]any{toolSearchEnabledKey: "on"}}},
		"off":        {toolsKey: map[string]any{toolSearchKey: map[string]any{toolSearchEnabledKey: "off"}}},
		"blank":      {toolsKey: map[string]any{toolSearchKey: map[string]any{toolSearchEnabledKey: "  "}}},
	} {
		t.Run(name, func(t *testing.T) {
			want := name
			if name == "tools-gone" || name == "blank" {
				want = "unset"
			}
			if got := toolSearchPolicy(doc); got != want {
				t.Errorf("toolSearchPolicy = %q, want %q", got, want)
			}
		})
	}
}

// writableSection is the guard that keeps a managed key from overwriting an
// operator value that is present but not a map.
func TestWritableSection(t *testing.T) {
	doc := map[string]any{"scalar": "value"}
	if _, ok := writableSection(doc, "scalar"); ok {
		t.Error("a present non-map value must not be replaced")
	}
	if _, present := doc["scalar"]; !present {
		t.Error("a refused write must not delete the value it refused to touch")
	}
	fresh, ok := writableSection(doc, "absent")
	if !ok {
		t.Fatal("an absent key is writable")
	}
	if len(fresh) != 0 {
		t.Errorf("a fresh section should start empty, got %v", fresh)
	}
	if _, present := doc["absent"]; present {
		t.Error("a fresh section must stay unattached until the caller writes it")
	}
}
