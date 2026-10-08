// SPDX-License-Identifier: AGPL-3.0-only

// Package jellyfinmcp wires jellyfin-mcp (Knuckles-Team/jellyfin-mcp) into
// Bloud as an MCP tool server over the media library Jellyfin already serves.
//
// jellyfin-mcp serves streamable HTTP itself, so there is no bridge process in
// front of it: this app is one container that speaks MCP on one port and talks
// the Jellyfin REST API out to apps-jellyfin. The wrapper is not a browser and
// cannot join the identity provider, so it reaches Jellyfin with a dedicated
// API key that Bloud mints on its behalf through the bootstrap admin
// credential the `mediaServer` contract publishes.
//
// The inbound side is authenticated too. The image verifies a bearer against a
// HMAC key it is configured with, so Bloud generates that key and mints the
// long-lived token it signs, and publishes the token as the `mcp` contract's
// httpToken. A harness therefore presents a credential this listener checks,
// not a name that authenticates against nothing.
//
// The image reads its configuration from process environment variables and
// nothing else, so the resolved bindings reach it through a generated env file
// declared as the container's `envFile`. That is the same shape apps/affine-mcp
// and apps/dav-mcp use; the sink differs only because this image cannot read a
// config file.
package jellyfinmcp

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// appName is the catalog ID (and secrets/registry key) for this app.
	appName = "jellyfin-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-jellyfin-mcp"
	// defaultPort is the server's bind port and the host-published port. It
	// must match metadata.yaml's `port`. 9334 sits next to dav-mcp's 9333,
	// the band the MCP wrappers occupy, and clear of every other catalog app.
	defaultPort = 9334

	// configDir is the subdirectory of the app data tree that holds the
	// generated env file, and envFileName the file itself. The directory is
	// not mounted into the container: the runtime reads the file on the host
	// and passes its values in at create time.
	configDir   = "config"
	envFileName = "env"

	// The environment variable names the image reads for the values that
	// `environment:` cannot render. Kept as constants so the renderer and the
	// tests name the same keys the upstream image expects.
	envJellyfinURL        = "JELLYFIN_URL"
	envJellyfinAPIKey     = "JELLYFIN_API_KEY"
	envVerificationSecret = "FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY"

	// The issuer and audience every inbound token must carry. These must match
	// FASTMCP_SERVER_AUTH_JWT_ISSUER and FASTMCP_SERVER_AUTH_JWT_AUDIENCE in
	// metadata.yaml: the verifier requires them, so a token minted with any
	// other pair is refused even when it is signed with the right key.
	jwtIssuer   = "bloud"
	jwtAudience = "jellyfin-mcp"

	// The provider side of this app: the Jellyfin catalog entry, the account
	// apps/jellyfin bootstraps (apps/jellyfin/configurator.go:bootstrapUsername,
	// the same account apps/seerr onboards from), and the name the API key this
	// app mints carries in Jellyfin's own Security screen.
	jellyfinAppName       = "jellyfin"
	jellyfinAdminUsername = "bloud-bootstrap-admin"
	jellyfinKeyName       = "jellyfin-mcp"

	// The secrets-store keys this app owns. `httpToken` is the published one,
	// declared under `provides.mcp.secrets`; the other two are private, which
	// is what keeps the HMAC key and the Jellyfin credential from landing in
	// some other consumer's binding.
	httpTokenKey          = "httpToken"
	verificationSecretKey = "jwtVerificationSecret"
	jellyfinAPIKeyKey     = "jellyfinApiKey"
)

// Configurator handles the jellyfin-mcp node lifecycle: mint the credentials
// this app owns, write the env file the container is created from (PreStart),
// and verify the server is serving MCP to an authenticated client (PostStart).
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	clients configurator.ClientFactory
	logger  *slog.Logger
	api     *mcpAPI

	// baseURL is a test seam: when set, the MCP probe resolves to it instead
	// of localhost:port.
	baseURL string
}

// NewConfigurator creates a new jellyfin-mcp configurator from the host Deps.
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
		clients: deps.HTTP,
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

// PreStart writes the env file the container spec declares as its envFile. It
// carries the four values `environment:` cannot render: the Jellyfin address
// from the `mediaServer` binding, the Jellyfin API key this app mints, the
// HMAC secret the inbound bearer is verified against, and the token that
// secret signs.
//
// Both credentials are generated on the first pass and reused afterwards, so
// a restart does not invalidate the namespace a harness registered, and the
// Jellyfin key is looked up by name before a new one is created, so a Bloud
// database reset adopts the key the server already has rather than stacking a
// second one under the same name.
//
// A binding that has not resolved yet is written as absent rather than as an
// empty value, and a later pass restarts the container once it lands. That
// keeps "not ready" distinguishable from "configured with nothing", which the
// app would otherwise read as the latter and fail on.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	secret, err := c.ensureVerificationSecret()
	if err != nil {
		return configurator.NoRestart(), err
	}
	if _, err := c.ensureInboundToken(secret); err != nil {
		return configurator.NoRestart(), err
	}

	server, wired := mediaServerBinding(state)
	apiKey := ""
	if wired {
		apiKey, err = c.ensureJellyfinAPIKey(ctx, server)
		if err != nil {
			// The wrapper's whole purpose is its tools, and a wrapper that
			// cannot get a credential into its target has nothing to serve.
			// Fail the pass so the self-healing pass retries rather than
			// promoting a server whose every tool call is about to fail.
			return configurator.NoRestart(), fmt.Errorf("provisioning the %s Jellyfin API key: %w", appName, err)
		}
	} else {
		c.logger.Warn("jellyfin-mcp: no media server bound yet; writing a config without a Jellyfin address")
	}

	content := renderEnvFile(server, wired, apiKey, secret)
	path := filepath.Join(state.DataPath, configDir, envFileName)

	// ModeHostOnly, not ModeSharedConfig: the file is never read inside the
	// container. The runtime reads it on the host and injects its values at
	// create time, so nothing needs the world-readable bit that a mounted
	// config file needs, and this file holds two live credentials.
	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeHostOnly)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote jellyfin-mcp config", "path", path,
		"mediaServer", wired, "apiKey", apiKey != "", "bearer", secret != "")
	return configurator.MustRestart("jellyfin-mcp config rewritten"), nil
}

// PostStart verifies the server answers the MCP handshake with the bearer a
// real consumer presents. The container's own /health is not enough: it
// returns 200 as long as the process is up, whether or not authentication is
// actually being enforced and whether or not the Jellyfin side works, so a
// probe against it promotes a node that cannot serve a single authorized MCP
// request. This POSTs a real `initialize` to the same path a harness calls,
// with the published token, and requires the server's own identity back.
//
// Because PostStart also runs on every resync pass, a server that stops
// serving after it converged is surfaced rather than left silently up.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	if _, wired := mediaServerBinding(state); !wired {
		return fmt.Errorf("waiting for the %s media server: no media server is bound, so this node would serve a tool surface with nothing behind it", appName)
	}
	if err := c.api.waitServing(ctx, c.currentBearer()); err != nil {
		return fmt.Errorf("waiting for the jellyfin-mcp server: %w", err)
	}
	c.logger.Info("jellyfin-mcp is serving MCP")
	return nil
}

// providerClient builds the client a configurator uses to reach a provider
// *from the host* (its own API calls): the binding's LocalURL, never the
// address stored for the app's containers.
func (c *Configurator) providerClient(ref configurator.ProviderRef) *appclient.Client {
	return c.clients.New(appclient.Spec{Name: ref.App, BaseURLFn: func() string {
		return ref.LocalURL
	}})
}

// currentBearer reads the MCP bearer this app publishes without generating
// one. PreStart owns generation; PostStart only has to present whatever is
// current, so a rotation landing between the two phases is still picked up.
func (c *Configurator) currentBearer() string {
	if c.secrets == nil {
		return ""
	}
	return c.secrets.GetAppSecret(appName, httpTokenKey)
}

// mediaServerBinding returns the installed Jellyfin provider, or false when
// the catalog declares none or it is not installed yet.
func mediaServerBinding(state *configurator.AppState) (configurator.MediaServerBinding, bool) {
	if state == nil {
		return configurator.MediaServerBinding{}, false
	}
	for _, binding := range state.Integrations.MediaServers {
		if binding.App == jellyfinAppName && binding.Installed && binding.AdminPassword != "" {
			return binding, true
		}
	}
	return configurator.MediaServerBinding{}, false
}
