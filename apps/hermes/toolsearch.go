// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// Tool Search is Hermes' progressive-disclosure layer for MCP tool schemas.
// When it is active, Hermes leaves the deferred tools out of the model-facing
// tools array and offers three bridge tools instead (`tool_search`,
// `tool_describe`, `tool_call`). The model loads a specific schema only when it
// needs one.
//
// Bloud is the reason Hermes needs it. Every installed MCP provider lands in
// `mcp_servers`, and the tool counts are large: `apps/affine-mcp` publishes 106
// tools, `apps/ha-mcp` 77, and `apps/dav-mcp` 27. Those three alone are 210
// schemas, and `apps/arr-mcp`, `apps/navidrome-mcp`, and `apps/penpot` add more
// on top. Hermes defers MCP tools by default, so the saving already happens.
// Bloud writes the pin anyway, for two reasons.
//
// First, the default is `auto`, and the upstream documentation says `auto` is an
// alias of `on` today and is reserved for a future budget-gated mode. It tells
// an integrator to pin `on` or `off` to keep today's behavior across upgrades.
// Bloud depends on that behavior for every MCP provider it wires, so Bloud
// states it rather than inheriting it.
//
// Second, the pin makes the disclosure layer visible in the file an operator
// reads. `mcp_servers` says which namespaces exist; `tools.tool_search` says
// how they reach the model. The two decisions belong in the same place.
//
// Sources: https://hermes-agent.nousresearch.com/docs/user-guide/features/tool-search
// and `tools/tool_search.py` plus `hermes_cli/config_defaults.py` in the pinned
// image.

const (
	// toolsKey is the Hermes config section holding tool policy.
	toolsKey = "tools"
	// toolSearchKey is the section within `tools` holding the Tool Search policy.
	toolSearchKey = "tool_search"
	// toolSearchEnabledKey is the one switch Bloud writes. The other keys
	// (`threshold_pct`, `listing`, `listing_max_tokens`, `defer`) stay with the
	// operator: they trade discovery quality against context, and Bloud has no
	// measurement that beats the upstream defaults for an arbitrary model.
	toolSearchEnabledKey = "enabled"
	// toolSearchEnabledValue is the pin. `on` is the documented guarantee;
	// `auto` is the default that promises only today's behavior.
	toolSearchEnabledValue = "on"
)

// applyToolSearch pins Tool Search on when this pass leaves at least one
// Bloud-wired MCP namespace in the config.
//
// It is adopt-once, the same rule `adoptDefaultModel` follows: an operator who
// set `tools.tool_search.enabled` keeps that value, including `off`. Bloud
// wires the namespaces, but the operator owns how many of them the model pays
// for.
//
// A section written as the legacy boolean shape (`tools: {tool_search: true}`)
// is also an operator statement, so it is left alone rather than rewritten into
// the map shape.
func applyToolSearch(doc map[string]any, state *configurator.AppState) {
	if len(mcpServerNames(state)) == 0 {
		stripToolSearch(doc)
		return
	}
	tools, toolsOK := writableSection(doc, toolsKey)
	search, searchOK := writableSection(tools, toolSearchKey)
	if !toolsOK || !searchOK {
		return
	}
	if existing, ok := search[toolSearchEnabledKey].(string); ok && strings.TrimSpace(existing) != "" {
		return
	}
	// Attach the sections only on the write path, so a pass that decides to do
	// nothing cannot leave an empty `tools: {}` behind and report a change.
	doc[toolsKey] = tools
	tools[toolSearchKey] = search
	search[toolSearchEnabledKey] = toolSearchEnabledValue
}

// stripToolSearch removes the pin when Bloud wired no MCP namespace this pass,
// and only when the value is the one Bloud wrote. An operator value survives,
// so removing the last MCP provider cannot silently re-enable a policy the
// operator had turned off.
//
// Empty sections are deleted with the pin, so a config with no tool policy has
// no empty `tools:` key rather than one Hermes reads as a malformed section.
func stripToolSearch(doc map[string]any) {
	tools, ok := doc[toolsKey].(map[string]any)
	if !ok {
		return
	}
	search, ok := tools[toolSearchKey].(map[string]any)
	if !ok {
		return
	}
	if value, ok := search[toolSearchEnabledKey].(string); !ok || value != toolSearchEnabledValue {
		return
	}
	delete(search, toolSearchEnabledKey)
	if len(search) == 0 {
		delete(tools, toolSearchKey)
	}
	if len(tools) == 0 {
		delete(doc, toolsKey)
	}
}

// writableSection returns doc[key] as a map an edit may write, and reports
// false when the key holds anything other than a map or nothing at all.
//
// Unlike `mapAt`, a value that is present but not a map is not replaced. That
// difference is the point here: `tools: {tool_search: true}` is valid Hermes
// config in the legacy shape, and a managed key must not overwrite an operator's
// statement of the same policy. The returned map may be fresh and unattached;
// the caller attaches it only when it writes.
func writableSection(doc map[string]any, key string) (map[string]any, bool) {
	if section, ok := doc[key].(map[string]any); ok {
		return section, true
	}
	if _, present := doc[key]; present {
		return nil, false
	}
	return map[string]any{}, true
}

// toolSearchPolicy names the Tool Search state for the config-change log line:
// `on`, `off`, `auto`, or `unset`. It reads the merged document, so it reports
// what Hermes will see rather than what Bloud wanted.
func toolSearchPolicy(doc map[string]any) string {
	tools, ok := doc[toolsKey].(map[string]any)
	if !ok {
		return "unset"
	}
	search, ok := tools[toolSearchKey].(map[string]any)
	if !ok {
		return "unset"
	}
	value, ok := search[toolSearchEnabledKey].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "unset"
	}
	return value
}
