// SPDX-License-Identifier: AGPL-3.0-only

// Package immichmcp wires the third-party ImmichMCP tool server into Bloud.
//
// The upstream image serves a streamable-HTTP MCP endpoint at /mcp with no
// inbound authentication at all. Bloud's `mcp` contract requires the published
// bearer to be a credential the provider itself validates, so the app runs the
// upstream behind a Caddy edge that requires that bearer and owns the app's
// published port. The token is real: Caddy rejects every /mcp request that does
// not carry it, which is the property the contract promises and the reason a
// bare wrapper is not shippable.
//
// The credential into Immich is the other direction and comes from Immich
// itself: Immich mints a scoped API key and publishes it under the `appToken`
// contract, and this configurator writes it into the appsettings.json the
// image reads at startup. Nothing is minted here, so PreStart stays offline and
// the key remains revocable in Immich's own API-keys screen.
//
// The key belongs to Bloud's internal Immich admin, not to the operator: Immich
// has no API to mint a key for another account, and the operator's account is
// an OIDC user whose password Bloud never sees. The wrapper therefore sees that
// admin's library. Linking the operator's SSO identity to the admin account is
// the follow-up; INTEGRATION.md records the boundary.
package immichmcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "immich-mcp"
	// nodeName is the graph node this configurator is registered under. It is
	// the Caddy edge, which metadata.yaml lists last so it is also the app's
	// primary node: the one the `mcp` binding's address points at and the one
	// whose PreStart writes both generated files.
	nodeName = "apps-immich-mcp"
	// defaultPort is the edge's published port and the app's catalog `port`.
	// It must stay clear of the host-agent's own 3000 and of affine-mcp's 9222.
	defaultPort = 9223

	// upstreamNode is the tool server the edge fronts. It publishes no port of
	// its own, so the only way to reach it is through the edge.
	upstreamNode = "apps-immich-mcp-upstream"
	// upstreamPort is the port the upstream image binds inside its container,
	// fixed by its own image (ASPNETCORE_URLS=http://+:5000, MCP_PORT=5000).
	upstreamPort = 5000

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"

	// configDirName is the app's own data subdirectory the two generated files
	// live in; metadata.yaml mounts each one into its container.
	configDirName = "config"
	// appSettingsFile is the .NET configuration file the upstream image reads
	// from its content root. It carries the Immich address and the minted key.
	appSettingsFile = "appsettings.json"
	// caddyfileName is the edge's config, mounted over the image's default.
	caddyfileName = "Caddyfile"
)

// Configurator handles the immich-mcp edge node lifecycle. Its PreStart writes
// the two generated files; its PostStart re-checks the upstream's config so a
// replacement Immich key reaches a wrapper that is already running.
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger

	// restart and running are the host-runtime callbacks from Deps. They exist
	// so a change to the upstream's config can be applied by restarting that
	// container; the edge's own config is applied by PreStart's recreate. Both
	// are nil in CLI/test contexts, where the restart degrades to "cannot apply
	// now".
	restart func(ctx context.Context, name string) error
	running func(ctx context.Context, name string) (bool, error)
}

// NewConfigurator creates a new immich-mcp configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName),
		restart: deps.RestartContainer,
		running: deps.ContainerRunning,
	}
}

func (c *Configurator) Name() string {
	return nodeName
}

// PreStart writes both generated files: the upstream's appsettings.json (the
// only channel for a credential the container cannot have in its environment)
// and the edge's Caddyfile (the bearer the `mcp` contract publishes).
//
// It generates and publishes the bearer on the first pass and reuses the stored
// one afterwards, so a restart does not invalidate the namespace a harness
// registered. It never mints the Immich key; that is Immich's, read from the
// resolved `appToken` binding the graph orders before this node.
//
// When no key has been published yet the file is still written, with an empty
// key, and every authenticated Immich call fails. That is deliberate: the
// upstream resolves its options on first use, so it comes up and answers its
// liveness probe, but `/health/ready` (which pings Immich) fails, the health
// phase fails, and the node is retried rather than reporting RUNNING while
// every tool call 401s. In practice the required `appToken` edge means the key
// is published before this runs; the empty case is what a failed mint looks
// like, and the self-healing pass re-drives it once Immich has published.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	token, err := c.ensureHTTPToken()
	if err != nil {
		return configurator.NoRestart(), err
	}

	key, baseURL, ok := appTokenBinding(state)
	if !ok {
		c.logger.Warn("immich-mcp: no published Immich appToken yet; the readiness probe will fail until Immich publishes one")
	}

	settings, err := renderAppSettings(baseURL, key)
	if err != nil {
		return configurator.NoRestart(), err
	}
	caddy, err := renderCaddyfile(upstreamNode, c.port, token)
	if err != nil {
		return configurator.NoRestart(), err
	}

	settingsChanged, err := writeConfig(state, appSettingsFile, settings)
	if err != nil {
		return configurator.NoRestart(), err
	}
	caddyChanged, err := writeConfig(state, caddyfileName, caddy)
	if err != nil {
		return configurator.NoRestart(), err
	}

	// The edge's config is applied by the recreate PreStart asks for below.
	// The upstream's is not: this node is the edge, so a changed appsettings
	// has to be applied by restarting the other container, which re-reads the
	// file at startup. On the first install the upstream does not exist yet and
	// picks the file up when it starts.
	if settingsChanged {
		c.restartUpstream(ctx)
	}

	if caddyChanged {
		return configurator.MustRestart("immich-mcp edge config rewritten"), nil
	}
	return configurator.NoRestart(), nil
}

// PostStart re-checks the upstream's config on the PostStart resync, which runs
// on every pass for a node already at RUNNING and is therefore the only path
// that notices a key Immich replaced after the wrapper came up. The edge's
// config is not touched here: its one variable is the MCP bearer, which changes
// only when the secrets store is cleared or the app is reinstalled, and both of
// those drive PreStart.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	key, baseURL, ok := appTokenBinding(state)
	if !ok {
		c.logger.Warn("immich-mcp: no published Immich appToken; leaving the running wrapper's config alone")
		return nil
	}

	settings, err := renderAppSettings(baseURL, key)
	if err != nil {
		return err
	}
	changed, err := writeConfig(state, appSettingsFile, settings)
	if err != nil {
		return err
	}
	if changed {
		c.logger.Info("immich-mcp upstream config changed; restarting it")
		c.restartUpstream(ctx)
	}
	return nil
}

// restartUpstream restarts the tool server so it re-reads appsettings.json. A
// missing container (first install) and a missing runtime callback (CLI/tests)
// are both "cannot apply now", not failures: the next real start reads the file.
func (c *Configurator) restartUpstream(ctx context.Context) {
	if c.restart == nil {
		return
	}
	if c.running != nil {
		running, err := c.running(ctx, upstreamNode)
		if err != nil || !running {
			return
		}
	}
	if err := c.restart(ctx, upstreamNode); err != nil {
		c.logger.Warn("could not restart the immich-mcp upstream to pick up its config", "err", err)
	}
}

// --- MCP bearer ---

// ensureHTTPToken returns the bearer the Caddy edge validates, generating and
// publishing one on the first pass and reusing it afterwards.
//
// The token is Bloud's to invent: the edge is a container Bloud configures, so
// the credential it checks is one it was told, which is what makes the
// published `mcp` token real rather than decorative. Persisting it is what
// keeps a harness's registered namespace valid across restarts.
func (c *Configurator) ensureHTTPToken() (string, error) {
	if c.secrets == nil {
		// CLI/test contexts have no store; the Caddyfile is written so that
		// every request but the liveness probe is refused, and the next real
		// pass fills the bearer in.
		return "", nil
	}
	if existing := c.secrets.GetAppSecret(appName, httpTokenKey); existing != "" {
		return existing, nil
	}
	token, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("generating the %s bearer: %w", appName, err)
	}
	if err := c.secrets.SetAppSecret(appName, httpTokenKey, token); err != nil {
		return "", fmt.Errorf("publishing the %s bearer: %w", appName, err)
	}
	c.logger.Info("generated the immich-mcp MCP bearer")
	return token, nil
}

// randomToken returns 32 bytes of entropy as URL-safe base64, whose alphabet
// contains nothing the Caddyfile or a JSON string would have to escape.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- Generated files ---

// writeConfig writes one generated file into the app's config directory.
// ModeHostOnly because both files can carry a credential and both containers
// run as root inside rootless podman, which reads the host writer's uid.
func writeConfig(state *configurator.AppState, name, content string) (bool, error) {
	path := filepath.Join(state.DataPath, configDirName, name)
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return false, fmt.Errorf("writing %s config: %w", appName, err)
	}
	return changed, nil
}

// appTokenBinding returns the one usable `appToken` provider. The contract is
// required and single-provider, so the first complete binding is the answer. A
// binding missing its token or address is the provider's "not published yet"
// state, not a usable credential, and writing it would configure a sign-in that
// cannot succeed.
func appTokenBinding(state *configurator.AppState) (token, baseURL string, ok bool) {
	if state == nil {
		return "", "", false
	}
	for _, b := range state.Integrations.AppTokens {
		if !b.Installed || b.Token == "" || b.BaseURL == "" {
			continue
		}
		return b.Token, strings.TrimSuffix(b.BaseURL, "/"), true
	}
	return "", "", false
}

// appSettings is the slice of the image's configuration Bloud owns. The JSON
// shape is the .NET configuration binding for `Immich:BaseUrl` and
// `Immich:ApiKey`: the image reads those keys from its content-root
// appsettings.json, which is where the generated file is mounted. Everything
// else (tool mode, page size, download mode) is static metadata in the
// container's environment.
type appSettings struct {
	Immich struct {
		BaseURL string `json:"BaseUrl"`
		APIKey  string `json:"ApiKey"`
	} `json:"Immich"`
}

// renderAppSettings renders the upstream's configuration. Order is fixed by the
// struct so an unchanged set of values produces byte-identical output, which is
// what stops the container being recreated on every pass.
//
// An empty key is written as an empty string rather than omitted. The image
// binds its options lazily, so it cannot validate the key at startup either
// way; the difference is the error a tool call reports, and an empty value
// keeps the generated file's shape stable across a pass that has no credential
// yet and one that does. What makes a missing key visible is the readiness
// probe, not the config file.
func renderAppSettings(baseURL, apiKey string) (string, error) {
	var doc appSettings
	doc.Immich.BaseURL = baseURL
	doc.Immich.APIKey = apiKey
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("rendering appsettings.json: %w", err)
	}
	return string(b) + "\n", nil
}

// renderCaddyfile renders the edge's config: it answers its own liveness probe,
// proxies an authorized request to the upstream, and refuses everything else.
//
// The liveness path is answered by Caddy itself instead of being proxied: the
// upstream `dependsOn` this container, so the edge must be able to become
// healthy while the tool server is still down, or the first install would wait
// on a health check that waits on the container it gates.
//
// An empty token leaves only the liveness route, so an app that has not yet
// published a bearer serves nothing an agent could call. That is the safe
// direction: a Caddyfile with a literal empty bearer would be a config with no
// credential at all.
func renderCaddyfile(upstream string, port int, token string) (string, error) {
	if strings.ContainsAny(token, "\"\\\r\n") {
		return "", fmt.Errorf("the %s bearer contains a character the Caddyfile cannot quote", appName)
	}

	var b strings.Builder
	b.WriteString("# Generated by Bloud; the host-agent rewrites this file on every\n")
	b.WriteString("# reconciliation and recreates the edge when its content changes.\n")
	b.WriteString("{\n")
	b.WriteString("\tadmin off\n")
	b.WriteString("\tauto_https off\n")
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, ":%d {\n", port)
	b.WriteString("\t@bloud-health path /health\n")
	b.WriteString("\thandle @bloud-health {\n")
	b.WriteString("\t\trespond \"ok\" 200\n")
	b.WriteString("\t}\n")
	if token != "" {
		fmt.Fprintf(&b, "\t@bloud-mcp header Authorization \"Bearer %s\"\n", token)
		b.WriteString("\thandle @bloud-mcp {\n")
		fmt.Fprintf(&b, "\t\treverse_proxy %s:%d\n", upstream, upstreamPort)
		b.WriteString("\t}\n")
	}
	b.WriteString("\thandle {\n")
	b.WriteString("\t\trespond \"unauthorized\" 401\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String(), nil
}
