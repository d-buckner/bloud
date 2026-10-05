// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// mcpState builds an app state carrying the given MCP bindings, with no
// inference binding so the MCP assertions are not entangled with the provider
// block.
func mcpState(dir string, bindings ...configurator.MCPBinding) *configurator.AppState {
	st := &configurator.AppState{DataPath: dir}
	st.Integrations.MCPServers = bindings
	return st
}

func writeExistingConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("seeding config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
}

func readConfigDoc(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
	if err != nil {
		t.Fatalf("reading written config: %v", err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written config is not valid yaml: %v", err)
	}
	return doc
}

func mcpHermes() *Configurator {
	return NewConfigurator(0, configurator.Deps{
		Logger:         quietLogger(),
		PrimaryBaseURL: func() string { return "http://localhost:8080" },
	})
}

func affineBinding() configurator.MCPBinding {
	return configurator.MCPBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "affine",
			Installed: true,
			BaseURL:   "http://apps-affine:3010",
			LocalURL:  "http://localhost:3010",
		},
		ServerName: "affine",
		Token:      "bearer-1",
		Path:       "/api/workspaces/ws-1/mcp",
	}
}

// The provider chain ends here: an installed MCP provider becomes a live tool
// namespace in the agent's own config, addressed by the host-loopback URL the
// harness can actually dial and authenticated by the provider's own bearer.
func TestPreStartWritesMCPServer(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	changed, err := c.PreStart(context.Background(), mcpState(dir, affineBinding()))
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("expected changed=true when adding an MCP server")
	}

	doc := readConfigDoc(t, dir)
	srv := nested(t, doc, mcpServersKey, "affine")
	want := "http://localhost:3010/api/workspaces/ws-1/mcp"
	if srv["url"] != want {
		t.Errorf("url = %v, want %s", srv["url"], want)
	}
	headers := nested(t, doc, mcpServersKey, "affine", "headers")
	if headers["Authorization"] != "Bearer bearer-1" {
		t.Errorf("Authorization = %v", headers["Authorization"])
	}
}

// LocalURL, not BaseURL. BaseURL is a podman network-scoped container name and
// Hermes runs in the host network namespace for its own OIDC reasons, so a URL
// built from BaseURL would not resolve from inside the harness.
func TestPreStartMCPServerUsesLocalURLNotBaseURL(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	b := affineBinding()
	b.LocalURL = ""
	if _, err := c.PreStart(context.Background(), mcpState(dir, b)); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	doc := readConfigDoc(t, dir)
	srv := nested(t, doc, mcpServersKey, "affine")
	if got, _ := srv["url"].(string); got != "http://apps-affine:3010/api/workspaces/ws-1/mcp" {
		t.Errorf("url = %v; with no LocalURL the BaseURL fallback is the only address left", got)
	}
}

// An uninstalled provider must not leave a tool namespace behind. Bindings
// arrive for every compatible provider an optional contract declares, so the
// Installed flag is the signal that the app is gone; skipping the entry rather
// than deleting it would leave the agent calling a dead server forever.
func TestPreStartRemovesUninstalledMCPServer(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart (install): %v", err)
	}
	if _, ok := readConfigDoc(t, dir)[mcpServersKey]; !ok {
		t.Fatal("precondition: the server was written")
	}

	gone := affineBinding()
	gone.Installed = false
	if _, err := c.PreStart(context.Background(), mcpState(dir, gone)); err != nil {
		t.Fatalf("PreStart (uninstall): %v", err)
	}

	if _, ok := readConfigDoc(t, dir)[mcpServersKey]; ok {
		t.Errorf("%s must be gone once its only provider is uninstalled", mcpServersKey)
	}
}

// A provider that is installed but has not published a credential yet must not
// register a namespace that 401s forever. The empty token is the same
// not-ready signal the provider side treats as "write nothing".
func TestPreStartSkipsUnpublishedToken(t *testing.T) {
	for name, mutate := range map[string]func(*configurator.MCPBinding){
		"empty token":  func(b *configurator.MCPBinding) { b.Token = "" },
		"empty path":   func(b *configurator.MCPBinding) { b.Path = "" },
		"no address":   func(b *configurator.MCPBinding) { b.LocalURL = ""; b.BaseURL = "" },
		"not install":  func(b *configurator.MCPBinding) { b.Installed = false },
		"empty server": func(b *configurator.MCPBinding) { b.ServerName = ""; b.App = "" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			b := affineBinding()
			mutate(&b)
			if _, err := mcpHermes().PreStart(context.Background(), mcpState(dir, b)); err != nil {
				t.Fatalf("PreStart: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml"))
			if err == nil && strings.Contains(string(raw), mcpServersKey) {
				t.Errorf("%s: a binding that cannot be dialed must write no server entry, got:\n%s", name, raw)
			}
		})
	}
}

// A provider whose serverName is empty still needs a stable key, or the entry
// would be written under "" and be unreachable by name.
func TestMCPServerKeyFallsBackToApp(t *testing.T) {
	b := affineBinding()
	b.ServerName = ""
	if got := mcpServerKey(b); got != "affine" {
		t.Errorf("mcpServerKey = %q, want affine", got)
	}
}

// Operator-added MCP servers are not Bloud's to touch. Only the namespace a
// provider's serverName claims is managed; a hand-written entry under any other
// name survives every pass.
func TestPreStartPreservesOperatorMCPServers(t *testing.T) {
	dir := t.TempDir()
	writeExistingConfig(t, dir, `mcp_servers:
  my-local-tools:
    command: uvx
    args: ["mcp-server-time"]
`)
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	doc := readConfigDoc(t, dir)
	operators := nested(t, doc, mcpServersKey)
	if _, ok := operators["my-local-tools"]; !ok {
		t.Error("the operator's own MCP server must survive a Bloud pass")
	}
	if _, ok := operators["affine"]; !ok {
		t.Error("the Bloud-provided server must be added alongside it")
	}
}

// Removing Bloud's entry must leave the operator's entry and the section
// intact, and must not delete the whole `mcp_servers` key while other entries
// remain.
func TestRemoveMCPServerKeepsSiblings(t *testing.T) {
	doc := map[string]any{
		mcpServersKey: map[string]any{
			"affine":         map[string]any{"url": "x"},
			"my-local-tools": map[string]any{"url": "y"},
		},
	}
	removeMCPServer(doc, "affine")

	kept := nested(t, doc, mcpServersKey)
	if _, ok := kept["affine"]; ok {
		t.Error("affine should have been removed")
	}
	if _, ok := kept["my-local-tools"]; !ok {
		t.Error("the operator entry must remain")
	}

	removeMCPServer(doc, "my-local-tools")
	if _, ok := doc[mcpServersKey]; ok {
		t.Error("an emptied mcp_servers section should be deleted, not left as an empty map")
	}
}

// Steady state: a second pass over an unchanged binding writes nothing, so the
// orchestrator does not recreate the container every reconciliation.
func TestPreStartMCPIdempotent(t *testing.T) {
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
		t.Error("an unchanged MCP server list must not report a change")
	}
}

// A rotated credential has to actually land in the file, or the harness keeps
// presenting the old bearer after the provider replaced it.
func TestPreStartPicksUpRotatedToken(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	rotated := affineBinding()
	rotated.Token = "bearer-2"
	changed, err := c.PreStart(context.Background(), mcpState(dir, rotated))
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("a rotated token must rewrite the config")
	}
	doc := readConfigDoc(t, dir)
	headers := nested(t, doc, mcpServersKey, "affine", "headers")
	if headers["Authorization"] != "Bearer bearer-2" {
		t.Errorf("Authorization = %v, want the rotated bearer", headers["Authorization"])
	}
}

// A workspace change moves the endpoint path, which is a different document
// even though the credential is unchanged.
func TestPreStartPicksUpWorkspaceChange(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}

	moved := affineBinding()
	moved.Path = "/api/workspaces/ws-2/mcp"
	if _, err := c.PreStart(context.Background(), mcpState(dir, moved)); err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	doc := readConfigDoc(t, dir)
	srv := nested(t, doc, mcpServersKey, "affine")
	if srv["url"] != "http://localhost:3010/api/workspaces/ws-2/mcp" {
		t.Errorf("url = %v, want the new workspace path", srv["url"])
	}
}

// caldavBinding is the second MCP provider: the calendar namespace, installed
// after the harness was already running.
func caldavBinding() configurator.MCPBinding {
	return configurator.MCPBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "dav-mcp",
			Installed: true,
			BaseURL:   "http://apps-dav-mcp:9333",
			LocalURL:  "http://localhost:9333",
		},
		ServerName: "dav-mcp",
		Token:      "caldav-bearer",
		Path:       "/mcp",
	}
}

// The install-order case the config resync exists for. Hermes was installed
// first, so its config.yaml was written with the one MCP namespace that
// existed at the time, and dav-mcp arrived afterwards. The next pass
// resolves the new binding, and PreStart has to both write the namespace and
// ask for the restart that makes Hermes re-read the file. While the resync ran
// PostStart only, this is exactly the pass that did nothing, and the calendar
// tools never appeared until Hermes was restarted by hand.
func TestPreStartPicksUpProviderInstalledLater(t *testing.T) {
	dir := t.TempDir()
	c := mcpHermes()

	if _, err := c.PreStart(context.Background(), mcpState(dir, affineBinding())); err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if hasNamespace(readConfigDoc(t, dir), "dav-mcp") {
		t.Fatal("the later provider must not be present before it is installed")
	}

	changed, err := c.PreStart(context.Background(), mcpState(dir, affineBinding(), caldavBinding()))
	if err != nil {
		t.Fatalf("PreStart: %v", err)
	}
	if !changed.RestartNeeded {
		t.Fatal("a provider installed after the harness must rewrite the config and restart Hermes to pick it up")
	}

	doc := readConfigDoc(t, dir)
	for _, name := range []string{"affine", "dav-mcp"} {
		if !hasNamespace(doc, name) {
			t.Errorf("%s missing from %s after its provider was installed", name, mcpServersKey)
		}
	}
	srv := nested(t, doc, mcpServersKey, "dav-mcp")
	if srv["url"] != "http://localhost:9333/mcp" {
		t.Errorf("url = %v, want the dav-mcp endpoint", srv["url"])
	}
}

// hasNamespace reports whether the rendered config carries a live entry for
// one MCP server name.
func hasNamespace(doc map[string]any, name string) bool {
	servers, ok := doc[mcpServersKey].(map[string]any)
	if !ok {
		return false
	}
	_, ok = servers[name]
	return ok
}

// A trailing slash on the provider address must not produce a doubled slash in
// the composed URL, which would be a 404 nothing downstream explains.
func TestMCPEntryTrimsTrailingSlash(t *testing.T) {
	b := affineBinding()
	b.LocalURL = "http://localhost:3010/"
	entry, ok := mcpServerEntry(b)
	if !ok {
		t.Fatal("expected a usable entry")
	}
	if entry["url"] != "http://localhost:3010/api/workspaces/ws-1/mcp" {
		t.Errorf("url = %v, want no doubled slash", entry["url"])
	}
}

func TestMCPBindingsNilState(t *testing.T) {
	// PreStart itself dereferences state.DataPath for the config path, so a nil
	// state is not a call the orchestrator ever makes. What has to be nil-safe
	// is the MCP read, so a state with no bindings cannot panic.
	if got := mcpBindings(nil); got != nil {
		t.Errorf("mcpBindings(nil) = %v, want nil", got)
	}
	dir := t.TempDir()
	if _, err := mcpHermes().PreStart(context.Background(), &configurator.AppState{DataPath: dir}); err != nil {
		t.Fatalf("PreStart with no bindings: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "data", "config.yaml")); err == nil {
		if strings.Contains(string(raw), mcpServersKey) {
			t.Error("no bindings means no mcp_servers section")
		}
	}
}
