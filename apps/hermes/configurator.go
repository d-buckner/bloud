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
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
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
	// agentAPIKeySecret is the name the `agentApi` contract names for its one
	// credential, and so the key this configurator publishes Hermes' agent
	// bearer under. It is not chosen here: it is the contract's own secret
	// name, restated as a constant so the publish call and the contract
	// registry cannot drift into two different spellings of the same thing.
	agentAPIKeySecret = "apiKey"
	// managedScopes is the OIDC scope set written into the self-hosted
	// provider block. openid/profile/email are what Hermes requests by
	// default; offline_access is the load-bearing one.
	//
	// Without it Authentik never issues a refresh token, so when the access
	// token expires the dashboard has nothing to rotate with:
	// middleware._attempt_refresh returns nil on the empty refresh token and
	// the client is thrown into a fresh SSO round trip (audit:
	// session_verify_failure reason=no_provider_recognises, then login_start
	// reason=auto_sso). In a browser that bounce is nearly invisible. In the
	// macOS Desktop app it is a login prompt on every token expiry, which at
	// Bloud's 5 minute default meant every five minutes.
	//
	// It must contain every entry of metadata.yaml's sso.scopes, which is
	// what makes the host-agent attach those scopes to the provider (a test
	// pins the two together).
	managedScopes = "openid profile email offline_access"
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
	configurator.Base
	logger *slog.Logger
	api    *hermesAPI

	// exec runs a command inside the running Hermes container. It is the
	// fallback reader for config.yaml, which the host agent cannot read once
	// the container owns it (see readConfig), and the way the memory provider's
	// plugin is installed. Nil in CLI/test contexts, where a denied host read has
	// no remedy and no plugin can be installed.
	exec configurator.ExecFunc

	// restartContainer stops and starts the container through the host runtime.
	// The memory provider's plugin is read when the gateway starts, and a plugin
	// installed while the gateway is already running is not loaded until it
	// restarts. Nil in CLI/test contexts.
	restartContainer func(ctx context.Context, name string) error

	// containerRunning reports whether the Hermes container is up. readConfig
	// uses it to tell "stopped, so the read cannot be attempted" from "running
	// and the read still failed", which is a real fault. Nil in CLI/test
	// contexts; readConfig then keeps its hard-error path.
	containerRunning configurator.ContainerRunningFunc

	// secrets publishes the agent API credential Hermes minted for itself so
	// consumers of the `agentApi` contract can read it. Nil in CLI/test
	// contexts, where there is no host store to publish into and the publish
	// step is skipped rather than failed.
	secrets configurator.AppSecretsProvider
}

// nodeName is this app's graph node and container name; see NodeLifecycle.Name.
const nodeName = "apps-hermes"

// NewConfigurator creates a new Hermes configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	c := &Configurator{
		Base:             configurator.NewBase(port, defaultPort, nodeName),
		logger:           configurator.AppLogger(deps.Logger, appName),
		exec:             deps.Runtime.Exec,
		containerRunning: deps.Runtime.ContainerRunning,
		restartContainer: deps.Runtime.RestartContainer,
		secrets:          deps.Secrets,
	}
	if deps.HTTP != nil {
		c.api = newAPI(deps.HTTP, c.BaseURLFn())
	}
	return c
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
		// The container read is only possible while the container is running.
		// A stopped container is the cold-start state after a reboot, not a
		// permissions fault, so report it as a skippable condition: the file
		// keeps the config it already had and PreStart does not fail the node.
		if c.containerNotRunning(ctx) {
			return nil, fmt.Errorf("%w: %s%s", errContainerNotRunning, configFileName, containerReadUnavailable)
		}
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

// errContainerNotRunning reports that config.yaml is readable only through the
// container and the container is stopped, so the read cannot be completed this
// pass. PreStart turns it into a skipped merge rather than a failure: the file
// already carries the last merged config, and the next pass reads it through
// the container once it is up (issue #184).
var errContainerNotRunning = errors.New("config is readable only through a running container")

// containerNotRunning reports whether the probe positively says the container
// is stopped. A nil probe or a probe error means "cannot tell" (false), so the
// caller keeps the hard error: an unverifiable state is not a reason to skip a
// merge that may be needed.
func (c *Configurator) containerNotRunning(ctx context.Context) bool {
	if c.containerRunning == nil {
		return false
	}
	running, err := c.containerRunning(ctx, nodeName)
	if err != nil {
		return false
	}
	return !running
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
	// Before the config merge, which can bail out on a stopped container: the inbox
	// has to exist for an upload to land at all, and a pass that returns early must
	// not be the reason it never gets created.
	if err := c.prepareInbox(state); err != nil {
		return configurator.NoRestart(), err
	}
	if err := c.prepareSharedDirs(state); err != nil {
		return configurator.NoRestart(), err
	}

	cfgPath := filepath.Join(state.DataPath, "data", configFileName)

	existing, err := c.readConfig(ctx, cfgPath)
	if errors.Is(err, errContainerNotRunning) {
		// Cold start: the container owns config.yaml and is not up yet, so the
		// merge cannot be measured against the current document. Leaving the
		// file alone starts Hermes on its last merged config instead of
		// parking the node in ERROR. If that config is stale, PostStart's
		// provider check fails and the next pass re-runs PreStart with the
		// container up, which is when the merge can complete.
		c.logger.Warn("skipping Hermes config merge: the container is stopped and the host cannot read its config; "+
			"keeping the config already on disk", "path", cfgPath)
		return configurator.NoRestart(), nil
	}
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
		applyOIDC(doc, state.OIDC, state.AppExternalURL(appName))
	} else {
		stripOIDC(doc)
	}

	binding, hasInference := inferenceBinding(state)
	if hasInference {
		applyInference(doc, binding)
		c.publishAgentModelName(binding.DefaultModel)
	} else {
		stripInference(doc)
	}

	applyMCPServers(doc, state)
	pruneRetiredMCPServers(doc, state)
	// After applyMCPServers: the pin is written only when a namespace survives
	// this pass, so the two managed sections cannot disagree about what the
	// agent actually has.
	applyToolSearch(doc, state)
	applyMemoryProvider(doc, state)

	written, err := c.writeManagedConfig(cfgPath, doc, base, state)
	if err != nil {
		return configurator.NoRestart(), err
	}
	if written.any() {
		c.logger.Info("updated Hermes config", "path", cfgPath, "sso", ssoActive, "inference", hasInference,
			"mcpServers", mcpServerNames(state), "toolSearch", toolSearchPolicy(doc),
			"memoryProvider", memoryProviderOf(doc), "memoryConfig", written.memory)
	}
	return configurator.RestartIf(written.any(), "Hermes config rewritten"), nil
}

// managedConfigWrite records what one pass had to change, so the phase can report
// it and decide whether the container has to read the files again.
//
// Two flags rather than one: they are two files, and the log line names which one
// moved. `memory` is separate because the memory provider's config carries a bearer
// that can rotate, and a rotation is the ordinary reason only that file changes.
type managedConfigWrite struct {
	config bool
	memory bool
}

func (w managedConfigWrite) any() bool { return w.config || w.memory }

// writeManagedConfig puts both managed files on disk: Hermes' own config, and the
// memory provider's settings beside it.
//
// The YAML is compared semantically against *base*, the document as it was
// parsed, so a re-parse-and-re-marshal of an unchanged file reports no change. The
// memory config is written on every pass instead: it holds the provider's address
// and a bearer, both of which can move while config.yaml is already right, and it
// compares its own bytes.
func (c *Configurator) writeManagedConfig(
	cfgPath string,
	doc map[string]any,
	base []byte,
	state *configurator.AppState,
) (managedConfigWrite, error) {
	var written managedConfigWrite

	want, err := yaml.Marshal(doc)
	if err != nil {
		return written, fmt.Errorf("serializing %s: %w", cfgPath, err)
	}
	if !bytes.Equal(base, want) {
		written.config, err = managedfile.Write(cfgPath, want, managedfile.ModeSharedConfig)
		if err != nil {
			return written, fmt.Errorf("writing %s: %w%s", cfgPath, err, permissionHint(err))
		}
	}

	if binding, ok := memoryBinding(state); ok {
		written.memory, err = c.writeMemoryConfig(state, binding)
		if err != nil {
			return written, fmt.Errorf("writing the memory provider config: %w", err)
		}
	}
	return written, nil
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

// retiredMCPServers are namespaces Bloud wrote and no longer wires, mapped to the
// reason. The add/remove rule above covers a provider that is declared but not
// installed; it cannot cover a provider Bloud stopped declaring, because nothing
// resolves for it any more and its entry would sit in the operator's config
// forever, pointing at a route the gate no longer serves.
//
// Deleting a name here is the same claim the wiring makes: the namespace is
// Bloud's, so a retired one is Bloud's to clean up. The value is not decoration.
// A name whose provider is wired again would delete a live entry, so a test
// proves each entry still names a namespace nothing offers; an entry that guards
// nothing is itself a failure, the same rule the catalog's exception tables
// follow.
var retiredMCPServers = map[string]string{
	"hindsight": "Hindsight serves the `memory` contract now; the harness activates it as its own memory provider",
}

// pruneRetiredMCPServers removes the entries above, and only when Bloud is not
// wiring that name this pass: a provider that came back keeps its entry.
func pruneRetiredMCPServers(doc map[string]any, state *configurator.AppState) {
	live := map[string]bool{}
	for _, name := range mcpServerNames(state) {
		live[name] = true
	}
	for name := range retiredMCPServers {
		if live[name] {
			continue
		}
		removeMCPServer(doc, name)
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
func mcpBindings(state *configurator.AppState) []bindings.MCPBinding {
	if state == nil {
		return nil
	}
	return state.Integrations.MCPServers
}

// mcpServerKey names the Hermes entry for one binding. serverName is the
// contract's own namespace label; the provider app name is the fallback so a
// provider that omitted it still lands somewhere identifiable.
func mcpServerKey(b bindings.MCPBinding) string {
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
func mcpServerEntry(b bindings.MCPBinding) (map[string]any, bool) {
	if !b.Installed || !b.Token.Published() || b.Path == "" {
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
//
// The memory provider is the second thing this phase owns, and for the opposite
// reason: it needs the container running to install its plugin, so it cannot wait
// for PreStart.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if err := c.api.waitDashboard(ctx); err != nil {
		return fmt.Errorf("waiting for hermes dashboard: %w", err)
	}
	if state != nil && state.SSOEnabled && state.OIDC != nil {
		if err := c.api.waitSelfHostedProvider(ctx); err != nil {
			return fmt.Errorf("verifying self-hosted OIDC provider: %w", err)
		}
		c.logger.Info("Hermes dashboard serving under Bloud SSO", "issuer", state.OIDC.IssuerURL)
		if err := c.publishAgentAPIKey(ctx); err != nil {
			return err
		}
	}
	return c.startMemoryProvider(ctx, state)
}

// startMemoryProvider installs the pinned plugin and restarts the container when it
// had to, which is the one step of the memory integration that cannot happen in
// PreStart: the plugin is installed with a command run inside the container, and
// the container is not up until this phase.
//
// The restart is what the vendor's own install message asks for ("Restart the
// gateway for the plugin to take effect"). It is bounded to the pass that
// installed the plugin: the record the installer writes is compared before
// anything runs, so a later pass finds the pin already in place and leaves the
// running agent alone.
//
// Everything here is best-effort except the restart itself. A clone that fails on
// a slow network leaves the agent running with the memory it had and is retried on
// the next pass, which is the honest outcome for an enhancement; a container that
// cannot be restarted after a successful install is a wiring fault, and says so.
func (c *Configurator) startMemoryProvider(ctx context.Context, state *configurator.AppState) error {
	binding, ok := memoryBinding(state)
	if !ok {
		return nil
	}
	installed, err := c.ensureMemoryPlugin(ctx, state, binding)
	if err != nil {
		c.logger.Warn("the Hermes memory plugin is not in place; the agent keeps its current memory configuration",
			"plugin", memoryPluginName(binding), "error", err)
		return nil
	}
	if !installed {
		return nil
	}
	if c.restartContainer == nil {
		return errors.New("the memory plugin was installed but no container restart is wired to load it")
	}
	c.logger.Info("installed the Hermes memory plugin; restarting the container so the gateway loads it",
		"plugin", memoryPluginName(binding), "commit", binding.PluginCommit)
	if err := c.restartContainer(ctx, nodeName); err != nil {
		return fmt.Errorf("the memory plugin is installed but the container could not be restarted: %w", err)
	}
	return nil
}

// memoryProviderOf names the memory provider choice for the config-change log
// line. It reads the merged document, so it reports what Hermes will see rather
// than what Bloud wanted.
func memoryProviderOf(doc map[string]any) string {
	memory, ok := doc[memoryKey].(map[string]any)
	if !ok {
		return "unset"
	}
	value, ok := memory[memoryProviderKey].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "unset"
	}
	return value
}

// agentAPIKeyEnvVar is the name Hermes stores its agent API bearer under in
// $HERMES_HOME/.env. The image generates one on first boot when none is
// present, and the gateway treats a usable key as the switch that starts the
// api_server adapter, so this is both the credential and the enable.
const agentAPIKeyEnvVar = "API_SERVER_KEY"

// agentModelNameValue is the `agentApi` contract value this configurator fills
// at runtime, carrying the default model Hermes is actually running so a
// consumer front end can preset its own picker to the same model.
const agentModelNameValue = "modelName"

// publishAgentModelName fills the agentApi contract's modelName value with the
// Bloud inference default model, so a consumer (e.g. hermes-webui) can preset
// its model picker to the same model instead of defaulting to a discovered
// list's arbitrary first entry.
//
// It is a no-op when there is no store or no default model. A missing model
// name is not a fault: the contract marks it optional precisely because an
// agent with no preference lets the consumer's own default stand.
func (c *Configurator) publishAgentModelName(defaultModel string) {
	if c.secrets == nil || defaultModel == "" {
		return
	}
	if existing := c.secrets.GetAppContractValue(appName, "agentApi", agentModelNameValue); existing == defaultModel {
		return // steady state: no write, so the resync stays a read-only diff
	}
	if err := c.secrets.SetAppContractValue(appName, "agentApi", agentModelNameValue, defaultModel); err != nil {
		c.logger.Warn("publishing the agent default model", "value", agentModelNameValue, "err", err)
		return
	}
	c.logger.Info("published the agent default model for the agentApi contract", "model", defaultModel)
}

// publishAgentAPIKey reads the agent API credential Hermes minted for itself
// and publishes it under the `agentApi` contract, so a consumer gets the real
// bearer rather than one Bloud invented and Hermes does not honor.
//
// The key is read rather than written. Hermes guards $HERMES_HOME/.env with
// 0600 owned by the container's mapped uid and re-tightens it on every boot,
// so a host-side write is EPERM after the first start and would fight the
// app forever. Reading it back through the container is the same channel
// config.yaml already uses for the same reason, and it keeps Hermes the
// authority over its own credential: Bloud relays it, it does not own it.
//
// A missing key is not fatal. The gateway simply has no API server, and
// consumers see no binding, which they treat as "not ready". Failing the
// node for that would take the dashboard down over an integration nobody is
// using yet.
func (c *Configurator) publishAgentAPIKey(ctx context.Context) error {
	if c.secrets == nil || c.exec == nil {
		return nil
	}
	key, ok := c.readEnvValue(ctx, agentAPIKeyEnvVar)
	if !ok || key == "" {
		c.logger.Warn("Hermes has not published an agent API key; the agentApi contract stays empty for consumers",
			"env", agentAPIKeyEnvVar)
		return nil
	}
	if existing := c.secrets.GetAppSecret(appName, agentAPIKeySecret); existing == key {
		return nil // steady state: no write, so the resync stays a read-only diff
	}
	if err := c.secrets.SetAppSecret(appName, agentAPIKeySecret, key); err != nil {
		return fmt.Errorf("publishing %s: %w", agentAPIKeySecret, err)
	}
	c.logger.Info("published Hermes agent API credential for the agentApi contract", "secret", agentAPIKeySecret)
	return nil
}

// readEnvValue pulls one KEY=value entry out of $HERMES_HOME/.env through the
// container. base64 keeps the exec channel byte-safe for a value that is hex
// but could carry anything, matching how config.yaml is read.
func (c *Configurator) readEnvValue(ctx context.Context, want string) (string, bool) {
	out, err := c.exec(ctx, nodeName, nil, []string{"base64", containerHome + "/.env"})
	if err != nil {
		c.logger.Debug("could not read Hermes .env through the container", "error", err)
		return "", false
	}
	decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if decErr != nil {
		return "", false
	}
	for _, line := range strings.Split(string(decoded), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(name) == want {
			return strings.Trim(strings.TrimSpace(value), "\"'"), true
		}
	}
	return "", false
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
func inferenceBinding(state *configurator.AppState) (bindings.InferenceBinding, bool) {
	if state == nil || len(state.Integrations.Inference) == 0 {
		return bindings.InferenceBinding{}, false
	}
	b := state.Integrations.Inference[0]
	if b.Endpoint == "" {
		return bindings.InferenceBinding{}, false
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
func applyInference(doc map[string]any, b bindings.InferenceBinding) {
	providers := mapAt(doc, inferenceProvidersKey)
	p := mapAt(providers, inferenceProviderKey)
	p["name"] = "Bloud"
	p["api_mode"] = inferenceAPIMode
	p["base_url"] = b.Endpoint
	if b.APIKey.Published() {
		p["api_key"] = b.APIKey.String()
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
