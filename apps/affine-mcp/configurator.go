// SPDX-License-Identifier: AGPL-3.0-only

// Package affinemcp wires the third-party affine-mcp-server into Bloud.
//
// AFFiNE ships its own MCP server and provides the `mcp` contract directly; a
// wrapper is the wrong shape for a target that already has both a server and a
// scoped-credential mechanism. This app exists because that built-in server is
// read-only and narrow, while affine-mcp-server exposes a much larger
// read-write tool surface. So Bloud runs both: the app keeps `affine`'s
// namespace, and this wrapper adds `affine-mcp`'s, and a harness picks.
//
// The wrapper is not a browser and cannot join the identity provider, so it
// consumes the `appApi` contract instead: AFFiNE publishes the bootstrap
// owner's username and password, and this configurator writes them into the
// config file the image reads at startup. The wrapper's own MCP bearer is the
// reverse direction: Bloud generates it, stores it, writes it into the same
// file, and publishes it under `provides.mcp`, so a harness authenticates
// against a credential this app actually validates.
package affinemcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "affine-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-affine-mcp"
	// defaultPort is the port the server binds and the host publishes. It must
	// match metadata.yaml's `port`: the orchestrator routes to the published
	// host port, and the binding's BaseURL addresses the container on the same
	// number. 9222 is chosen to stay clear of the host-agent's own 3000.
	defaultPort = 9222

	// configDirName is the app's own data subdirectory that metadata.yaml mounts
	// at /data/config. Under it the image looks for `affine-mcp/config` because
	// XDG_CONFIG_HOME is set to the mount point.
	configDirName = "config"
	// configSubdir is the fixed directory the image appends to
	// XDG_CONFIG_HOME/affine-mcp/config.
	configSubdir = "affine-mcp"
	// configFileName is the saved-config filename (no extension).
	configFileName = "config"

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"
)

// Configurator handles the affine-mcp-server node lifecycle. It has one job in
// each phase: write the config file the image reads at startup (PreStart), and
// verify the running server can reach AFFiNE (PostStart).
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *mcpAPI

	// baseURL is a test seam: when set, the API client resolves to it instead of
	// localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a new affine-mcp configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", appName),
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.port)
	})
	return c
}

func (c *Configurator) Name() string {
	return nodeName
}

// PreStart writes the image's saved config so the container comes up already
// authenticated to AFFiNE and already accepting Bloud's MCP bearer.
//
// It generates the bearer on the first pass and reuses the stored one
// afterwards, so a restart does not invalidate the namespace a harness
// registered. The AFFiNE credential and the default workspace come from the
// resolved `appApi` binding, which the graph orders before this node; if it is
// not published yet the file is still written without it, and the server starts
// (unauthenticated and unpinned) while a later pass restarts it once the
// credential lands. Erroring here instead would make the node terminal on a
// purely transient ordering condition.
//
// Failures to publish the bearer are returned: the wrapper's entire purpose is
// its MCP endpoint, so a node that cannot hand a harness a credential is
// genuinely broken and should retry rather than start unreachable.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	token, err := c.ensureHTTPToken()
	if err != nil {
		return configurator.NoRestart(), err
	}

	binding, hasBinding := affineBinding(state)
	if !hasBinding {
		c.logger.Warn("affine-mcp: no published affine appApi credential yet; writing an unauthenticated config")
	}

	content, err := renderConfig(binding, hasBinding, token)
	if err != nil {
		return configurator.NoRestart(), err
	}

	// The image runs as its own non-root user, a subordinate uid under rootless
	// podman that is not the host uid writing this file, so 0600 would leave the
	// server unable to read its own config. ModeSharedConfig bounds the
	// credential by the app's data directory instead of by the file mode.
	path := filepath.Join(state.DataPath, configDirName, configSubdir, configFileName)
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote affine-mcp config", "path", path, "affineCredential", hasBinding)
	return configurator.MustRestart("affine-mcp config rewritten"), nil
}

// PostStart verifies the running server reports its AFFiNE GraphQL endpoint
// reachable.
//
// Unlike AFFiNE, a wrapper that cannot reach its target is not doing its job:
// its whole purpose is the MCP endpoint, so a failing readiness probe is
// returned rather than swallowed and the node is retried by the self-healing
// pass. The probe is unauthenticated (`/readyz`), so a rejection here is a
// transport or config fault, never a missing harness bearer.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.api.waitReady(ctx); err != nil {
		return fmt.Errorf("waiting for the affine-mcp tool server: %w", err)
	}
	c.logger.Info("affine-mcp is serving MCP")
	return nil
}

// --- MCP bearer ---

// ensureHTTPToken returns the bearer this app validates, generating and
// publishing one on the first pass and reusing it afterwards.
//
// The token is Bloud's to invent, unlike AFFiNE's minted MCP credential: the
// wrapper authenticates its listener with a static shared secret it is told,
// not one it issues itself, so the rule "the published token is always a
// credential the provider validates" holds by construction. Persisting it is
// what keeps a harness's registered namespace valid across restarts.
func (c *Configurator) ensureHTTPToken() (string, error) {
	if c.secrets == nil {
		// CLI/test contexts have no store; the config is written without a
		// bearer rather than failing, and the next real pass fills it in.
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
	c.logger.Info("generated the affine-mcp MCP bearer")
	return token, nil
}

// randomToken returns 32 bytes of entropy as URL-safe base64. The config file
// is line-based, so the alphabet deliberately excludes anything that could
// terminate a line.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// affineBinding returns the one usable `appApi` provider. The contract is
// required and single-provider, so the first complete binding is the answer. A
// binding missing its password or username is the provider's "not published
// yet" state, not a usable credential, and writing it would configure a sign-in
// that cannot succeed.
func affineBinding(state *configurator.AppState) (configurator.AppAPIBinding, bool) {
	if state == nil {
		return configurator.AppAPIBinding{}, false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || b.BaseURL == "" || b.Username == "" || b.Password == "" {
			continue
		}
		return b, true
	}
	return configurator.AppAPIBinding{}, false
}

// renderConfig renders the image's key=value saved config. Order is fixed so an
// unchanged set of values produces byte-identical output and PreStart reports no
// change, which is what stops the container being recreated on every pass.
//
// The parser reads a line up to its first '=' and takes the rest verbatim, so a
// value containing a newline would inject a second setting. Both values here are
// machine-generated (a base64 password and token) or an operator email the app
// validates, but the guard is cheap and makes the file's integrity a property of
// this function rather than of upstream's input validation.
func renderConfig(binding configurator.AppAPIBinding, hasBinding bool, token string) (string, error) {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; the host-agent rewrites this file on every\n")
	b.WriteString("# reconciliation and restarts the container when its content changes.\n")

	settings := make([][2]string, 0, 5)
	if hasBinding {
		settings = append(settings,
			[2]string{"AFFINE_BASE_URL", binding.BaseURL},
			[2]string{"AFFINE_EMAIL", binding.Username},
			[2]string{"AFFINE_PASSWORD", binding.Password},
		)
		// Pin the workspace Bloud provisions. Every workspace-scoped tool in the
		// wrapper resolves `args.workspaceId || AFFINE_WORKSPACE_ID`, so without
		// this an agent has to list workspaces and choose one on every call, and
		// can choose the wrong one. A provider that published no scope leaves
		// this unset, which is the wrapper's documented "agent supplies the id"
		// mode.
		if binding.WorkspaceID != "" {
			settings = append(settings, [2]string{"AFFINE_WORKSPACE_ID", binding.WorkspaceID})
		}
	}
	settings = append(settings, [2]string{"AFFINE_MCP_AUTH_MODE", "bearer"})
	if token != "" {
		settings = append(settings, [2]string{"AFFINE_MCP_HTTP_TOKEN", token})
	}

	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "\r\n") {
			return "", fmt.Errorf("config value for %s contains a line break", kv[0])
		}
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return b.String(), nil
}
