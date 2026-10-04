// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// appName is the catalog ID (and secrets/registry key) for Hermes.
	appName = "hermes"
	// defaultPort is the Hermes dashboard port (upstream default).
	defaultPort = 9119
	// configFileName is Hermes' own config file, at $HERMES_HOME/config.yaml.
	// $HERMES_HOME is /opt/data, mounted from {{appDataDir}}/data, so on the
	// host this is <DataPath>/data/config.yaml.
	configFileName = "config.yaml"
	// containerHome is $HERMES_HOME inside the image, the mount point of the
	// host directory above. Reads that go through the container address the
	// file by its in-container path, not the host path.
	containerHome = "/opt/data"
	// managedScopes is the OIDC scope set written into the self-hosted
	// provider block (matches the Hermes default; stated explicitly so the
	// managed block is unambiguous).
	managedScopes = "openid profile email"
)

// Configurator handles the Hermes node lifecycle. Hermes owns most of
// $HERMES_HOME (it seeds config, memory, skills, the session store on first
// boot), so the configurator does not manage the whole file: it merges the
// Bloud-owned SSO keys into Hermes' config.yaml before the container starts
// (PreStart) and verifies the dashboard boots with the self-hosted OIDC
// provider active afterwards (PostStart).
//
// The OIDC issuer/client_id are per-install values, and the container spec
// cannot render them (only {{dataDir}}/{{appDataDir}}/static TemplateVars
// are available there), so they are delivered through the config file rather
// than container env.
type Configurator struct {
	port       int
	ssoBaseURL func() string // current Bloud base URL (host-set aware; read each PreStart)
	logger     *slog.Logger
	api        *hermesAPI

	// exec runs a command inside the running Hermes container. It is the
	// fallback reader for config.yaml, which the host agent cannot read once
	// the container owns it (see readConfig). Nil in CLI/test contexts, where
	// a denied host read has no remedy.
	exec configurator.ExecFunc

	// baseURL is a test seam: when set, the API client resolves to it
	// instead of localhost:port.
	baseURL string
}

// NewConfigurator creates a new Hermes configurator from the host Deps.
// deps.PrimaryBaseURL supplies the current Bloud base URL (e.g.
// "http://localhost:8080"); the dashboard's public URL is derived from it
// the same way routes and OIDC redirect URIs are (hermes.<host>). It is a
// function so host changes made in the UI take effect on the next pass.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:       port,
		ssoBaseURL: deps.PrimaryBaseURL,
		logger:     logger.With("app", "hermes"),
		exec:       deps.Exec,
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

// nodeName is this app's graph node and container name; see NodeLifecycle.Name.
const nodeName = "apps-hermes"

func (c *Configurator) Name() string {
	return nodeName
}

// appExternalURL returns the public URL the browser uses to reach Hermes,
// e.g. "http://hermes.localhost:8080". The dashboard derives its OIDC
// callback (<public_url>/auth/callback) from this, so it must match the
// redirect URI the host-agent registered with Authentik.
func (c *Configurator) appExternalURL() string {
	baseURL := ""
	if c.ssoBaseURL != nil {
		baseURL = c.ssoBaseURL()
	}
	if baseURL == "" {
		return "http://hermes.localhost:8080"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return "http://hermes.localhost:8080"
	}
	parsed.Host = appName + "." + parsed.Host
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return strings.TrimSuffix(parsed.String(), "/")
}

// readConfig reads Hermes' config file, falling back to the container when
// the host-side read is refused.
//
// Hermes' own container init (/opt/hermes/docker/stage2-hook.sh) chowns
// config.yaml to the runtime user and chmods it 0640 on every start, so that
// a file edited on the host stays readable by Hermes. Under rootless podman
// that runtime user is a host uid inside the subuid range (109999 for a
// uid-1000 agent) and the group is the matching subgid, so the mode leaves the
// host agent, which wrote the file, unable to read it back. The directory
// stays shared via HERMES_HOME_MODE, so the write path is fine; only the read
// needs the container, which reads its own file without complaint.
//
// The bytes come back base64-encoded on purpose. Deps.Exec is a combined
// stdout+stderr channel, so a podman warning would otherwise land inside the
// YAML. Encoded, contamination fails the decode instead of silently writing a
// corrupted config back over the real one.
func (c *Configurator) readConfig(ctx context.Context, cfgPath string) ([]byte, error) {
	raw, err := os.ReadFile(cfgPath)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return nil, err
	}
	if c.exec == nil {
		return nil, fmt.Errorf("%w%s", err, containerReadUnavailable)
	}

	out, execErr := c.exec(ctx, nodeName, nil, []string{"base64", containerHome + "/" + configFileName})
	if execErr != nil {
		return nil, fmt.Errorf("%w%s (reading it inside %s failed too: %v)",
			err, containerReadUnavailable, nodeName, execErr)
	}
	decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if decErr != nil {
		return nil, fmt.Errorf("decoding %s read from %s: %w", configFileName, nodeName, decErr)
	}
	c.logger.Info("read Hermes config through the container: the host agent cannot read a file the container owns",
		"path", cfgPath)
	return decoded, nil
}

// containerReadUnavailable is appended when a host read is refused and there
// is no container to read through. It names the mechanism, because "permission
// denied" on a file the agent itself wrote reads like a Bloud bug rather than
// the consequence of the app reclaiming its config file at boot.
const containerReadUnavailable = " (Hermes' container init chowns config.yaml to its own runtime user at 0640 on " +
	"every start, which under rootless podman is a host uid in the subuid range the agent is not; the file can only " +
	"be read through the running container - see apps/hermes/INTEGRATION.md)"

// PreStart merges Bloud's SSO keys into Hermes' config.yaml so the
// dashboard boots with the self-hosted OIDC provider configured. Returns
// changed=true only when the managed keys differ from what is on disk, which
// makes the orchestrator (re)create the container so Hermes re-reads it.
//
// The merge is whole-file and semantically compared: Hermes' other settings
// are preserved untouched, and a file that already carries the right SSO
// values produces no write (no churn across reconciliation cycles). When
// SSO is disabled the managed keys are stripped instead, so a leftover
// provider never points at a dead issuer.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	cfgPath := filepath.Join(state.DataPath, "data", configFileName)

	existing, err := c.readConfig(ctx, cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return configurator.NoRestart(), fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	// The managed edit is measured against the parsed document, not the raw
	// bytes: a re-parse-and-re-marshal of an unchanged file yields the same
	// document, so only a real SSO change reports changed=true.
	doc, err := parseConfig(existing)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("parsing %s: %w", cfgPath, err)
	}
	base, err := yaml.Marshal(doc)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("serializing %s: %w", cfgPath, err)
	}

	ssoActive := state != nil && state.SSOEnabled && state.OIDC != nil
	if ssoActive {
		applyOIDC(doc, state.OIDC, c.appExternalURL())
	} else {
		stripOIDC(doc)
	}

	binding, hasInference := inferenceBinding(state)
	if hasInference {
		applyInference(doc, binding)
	} else {
		stripInference(doc)
	}

	applyMCPServers(doc, state)

	want, err := yaml.Marshal(doc)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("serializing %s: %w", cfgPath, err)
	}
	if bytes.Equal(base, want) {
		return configurator.NoRestart(), nil
	}

	changed, err := managedfile.Write(cfgPath, want, managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s: %w%s", cfgPath, err, permissionHint(err))
	}
	if changed {
		c.logger.Info("updated Hermes config", "path", cfgPath, "sso", ssoActive, "inference", hasInference,
			"mcpServers", mcpServerNames(state))
	}
	return configurator.RestartIf(changed, "Hermes config rewritten"), nil
}

// --- MCP servers ---

// mcpServersKey is the Hermes config section holding the agent's MCP servers.
// Hermes reads it at startup and again on a `reload-mcp` chore, so a rewrite
// takes effect on the next container start.
const mcpServersKey = "mcp_servers"

// applyMCPServers renders the Bloud-provided MCP servers into Hermes'
// `mcp_servers` map, one entry per provider under that provider's serverName.
//
// Entries the operator added by hand under a different name are left exactly as
// they are, the same way applyInference leaves a hand-chosen model alone. What
// Bloud claims is the namespace: the key a provider's `serverName` names is
// Bloud's to write and to remove, because that is the app the integration
// contract points at.
//
// The removal case matters as much as the add. Bindings arrive for every
// compatible provider an optional contract declares, installed or not, so an
// uninstalled provider still shows up here with Installed false. Deleting its
// entry on that signal is what stops a removed app from lingering as a tool
// namespace the agent keeps trying to call.
func applyMCPServers(doc map[string]any, state *configurator.AppState) {
	for _, b := range mcpBindings(state) {
		name := mcpServerKey(b)
		if name == "" {
			continue
		}
		entry, ok := mcpServerEntry(b)
		if !ok {
			removeMCPServer(doc, name)
			continue
		}
		mapAt(doc, mcpServersKey)[name] = entry
	}
}

// removeMCPServer drops one Bloud-claimed entry and tidies the section away
// when nothing is left in it, so a config with no MCP servers has no empty
// `mcp_servers:` key rather than a key Hermes reads as a malformed entry.
func removeMCPServer(doc map[string]any, name string) {
	servers, ok := doc[mcpServersKey].(map[string]any)
	if !ok {
		return
	}
	delete(servers, name)
	if len(servers) == 0 {
		delete(doc, mcpServersKey)
	}
}

// mcpServerNames lists the namespaces that will be live after this pass, for
// the config-change log line. A provider that is present but not usable is
// absent from the list, which is the thing an operator needs to see.
func mcpServerNames(state *configurator.AppState) []string {
	names := []string{}
	for _, b := range mcpBindings(state) {
		if _, ok := mcpServerEntry(b); !ok {
			continue
		}
		if name := mcpServerKey(b); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// mcpBindings returns the resolved MCP bindings from the app state.
func mcpBindings(state *configurator.AppState) []configurator.MCPBinding {
	if state == nil {
		return nil
	}
	return state.Integrations.MCPServers
}

// mcpServerKey names the Hermes entry for one binding. serverName is the
// contract's own namespace label; the provider app name is the fallback so a
// provider that omitted it still lands somewhere identifiable.
func mcpServerKey(b configurator.MCPBinding) string {
	if b.ServerName != "" {
		return b.ServerName
	}
	return b.App
}

// mcpServerEntry builds the Hermes entry for one usable binding, or ok false
// when the binding cannot be dialed.
//
// LocalURL is the address, not BaseURL. BaseURL is `http://<container>:<port>`,
// which podman's network-scoped DNS serves only to containers on that network,
// and Hermes runs in the host network namespace for its own OIDC reasons. The
// host loopback is the address both sides agree on, and it is the same one
// Traefik uses to reach every app.
func mcpServerEntry(b configurator.MCPBinding) (map[string]any, bool) {
	if !b.Installed || b.Token == "" || b.Path == "" {
		return nil, false
	}
	base := b.LocalURL
	if base == "" {
		base = b.BaseURL
	}
	if base == "" {
		return nil, false
	}
	return map[string]any{
		"url": strings.TrimSuffix(base, "/") + b.Path,
		"headers": map[string]any{
			"Authorization": "Bearer " + b.Token,
		},
	}, true
}

// PostStart waits for the dashboard to serve and then, when SSO is
// configured, verifies the dashboard came up with the self-hosted OIDC
// provider registered (not the bundled password provider, and not an
// accidental loopback bind with the gate off). A green pass means the Bloud
// OIDC integration is the thing standing in front of this dashboard.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.api.waitDashboard(ctx); err != nil {
		return fmt.Errorf("waiting for hermes dashboard: %w", err)
	}
	if state == nil || !state.SSOEnabled || state.OIDC == nil {
		return nil
	}
	if err := c.api.waitSelfHostedProvider(ctx); err != nil {
		return fmt.Errorf("verifying self-hosted OIDC provider: %w", err)
	}
	c.logger.Info("Hermes dashboard serving under Bloud SSO", "issuer", state.OIDC.IssuerURL)
	return nil
}

// permissionHint names the failure this app is prone to. A bare "permission
// denied" on a bind-mounted data directory says nothing about who owns it or
// which side has to change: the directory belongs to the container's uid
// mapped into the rootless podman subuid range, so neither the read nor the
// temp-file write in that directory can succeed once Hermes secures it. The
// fix is the app's own mode contract (HERMES_CONTAINER / HERMES_HOME_MODE in
// metadata.yaml), not a host-side chmod, which is EPERM against a
// subordinate uid. Kept a hard error on purpose: a degraded "running with
// last-known config" would hide an SSO integration that is not being
// updated, which is worse than a node that says it failed.
func permissionHint(err error) string {
	if !errors.Is(err, fs.ErrPermission) {
		return ""
	}
	return " (the Hermes data directory is not writable by the host agent; " +
		"set HERMES_CONTAINER=1 and HERMES_HOME_MODE=0777 in metadata.yaml - see apps/hermes/INTEGRATION.md)"
}

// parseConfig decodes a Hermes config.yaml into a generic map. A missing or
// empty file is an empty document (Hermes fills defaults at runtime).
func parseConfig(raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return doc, nil
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// applyOIDC sets the Bloud-managed self-hosted OIDC keys, creating the
// intermediate maps as needed without disturbing any other keys.
func applyOIDC(doc map[string]any, oidc *configurator.OIDCOutput, publicURL string) {
	dash := mapAt(doc, "dashboard")
	oauth := mapAt(dash, "oauth")
	oauth["provider"] = "self-hosted"
	sh := mapAt(oauth, "self_hosted")
	sh["issuer"] = oidc.IssuerURL
	sh["client_id"] = oidc.ClientID
	sh["scopes"] = managedScopes
	dash["public_url"] = publicURL
}

// stripOIDC removes the Bloud-managed SSO keys when SSO is off. A leftover
// provider would otherwise make the gated dashboard depend on a dead issuer;
// with it gone the operator can re-enable SSO (which re-adds the block) or
// run a loopback-only dashboard.
func stripOIDC(doc map[string]any) {
	dash, ok := doc["dashboard"].(map[string]any)
	if !ok {
		return
	}
	delete(dash, "public_url")
	if oauth, ok := dash["oauth"].(map[string]any); ok {
		delete(oauth, "self_hosted")
		delete(oauth, "provider")
		if len(oauth) == 0 {
			delete(dash, "oauth")
		}
	}
	if len(dash) == 0 {
		delete(doc, "dashboard")
	}
}

// inferenceProviderKey is the named provider entry Bloud registers in Hermes'
// config.yaml, under the v12 `providers:` map.
const inferenceProviderKey = "bloud"

// inferenceProviderSlug is the identity Hermes uses to *select* that entry.
// A named custom provider is addressed as `custom:<config key>`, never as bare
// `custom`: bare `custom` reads OPENAI_BASE_URL/OPENAI_API_KEY from the
// environment and never looks at `providers:` at all, so a Bloud endpoint
// selected that way resolves to Hermes' OpenRouter default with no key and the
// agent dies at init with "No LLM provider configured". Hermes derives the
// slug from the config key rather than the display name, so it survives a
// rename of the display name (custom_provider_slug in hermes_cli/providers.py).
const inferenceProviderSlug = "custom:" + inferenceProviderKey

// inferenceAPIMode names the wire protocol explicitly. Without it Hermes
// infers the transport from the endpoint hostname, which works today but is a
// default Bloud should not be relying on for an endpoint it chose itself.
const inferenceAPIMode = "chat_completions"

// inferenceProvidersKey is the v12 config section holding named provider
// entries. Hermes also accepts a `custom_providers:` list alongside it and
// merges the two views at runtime; Bloud writes only the map.
const inferenceProvidersKey = "providers"

// inferenceBinding pulls the resolved inference binding out of the app state.
// A consumer with no binding has nothing to point at, which is different from a
// binding with an empty endpoint: the latter is a misconfiguration the resolver
// would not have produced.
func inferenceBinding(state *configurator.AppState) (configurator.InferenceBinding, bool) {
	if state == nil || len(state.Integrations.Inference) == 0 {
		return configurator.InferenceBinding{}, false
	}
	b := state.Integrations.Inference[0]
	if b.Endpoint == "" {
		return configurator.InferenceBinding{}, false
	}
	return b, true
}

// applyInference registers the Bloud provider block and adopts the instance
// default model only where Hermes has not chosen one.
//
// The provider block is always written when a binding exists, even if the
// operator has pointed Hermes somewhere else: keeping it registered means the
// operator can switch back in the dashboard without re-entering an endpoint.
// The model selection is the part that respects an existing choice, because
// silently replacing a model an operator picked is the failure mode that makes
// a managed default unwelcome.
func applyInference(doc map[string]any, b configurator.InferenceBinding) {
	providers := mapAt(doc, inferenceProvidersKey)
	p := mapAt(providers, inferenceProviderKey)
	p["name"] = "Bloud"
	p["api_mode"] = inferenceAPIMode
	p["base_url"] = b.Endpoint
	if b.APIKey != "" {
		p["api_key"] = b.APIKey
	} else {
		delete(p, "api_key")
	}
	if b.DefaultModel != "" {
		p["default_model"] = b.DefaultModel
	}
	// Ask Hermes to keep the model list current from the endpoint rather than
	// trusting a snapshot, so a model added upstream is reachable without a
	// Bloud change.
	p["discover_models"] = true

	adoptDefaultModel(doc, b.DefaultModel)
}

// adoptDefaultModel sets Hermes' active model to the Bloud default only when
// Hermes has no model selection of its own. An existing model.provider is left
// exactly as the operator set it.
func adoptDefaultModel(doc map[string]any, defaultModel string) {
	if defaultModel == "" {
		return
	}
	model := mapAt(doc, "model")
	if existing, ok := model["provider"].(string); ok && existing != "" {
		return
	}
	model["provider"] = inferenceProviderSlug
	model["model"] = defaultModel
}

// stripInference removes the Bloud-managed provider block, and the model
// selection only when that selection is the one Bloud wrote. An operator-chosen
// model pointing somewhere else is left alone, so removing Bloud's inference
// cannot break a configuration the operator built by hand.
func stripInference(doc map[string]any) {
	providers, ok := doc[inferenceProvidersKey].(map[string]any)
	if ok {
		delete(providers, inferenceProviderKey)
		if len(providers) == 0 {
			delete(doc, inferenceProvidersKey)
		}
	}

	model, ok := doc["model"].(map[string]any)
	if !ok {
		return
	}
	active, _ := model["model"].(string)
	provider, _ := model["provider"].(string)
	if provider != inferenceProviderSlug || active == "" {
		return
	}
	delete(model, "model")
	delete(model, "provider")
	if len(model) == 0 {
		delete(doc, "model")
	}
}

// mapAt returns doc[key] as a mutable map[string]any, replacing any
// non-map value (or absent key) with a fresh map.
func mapAt(doc map[string]any, key string) map[string]any {
	if m, ok := doc[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	doc[key] = m
	return m
}
