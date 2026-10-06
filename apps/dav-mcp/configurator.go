// SPDX-License-Identifier: AGPL-3.0-only

// Package davmcp wires dav-mcp (PhilflowIO/dav-mcp) into Bloud as an MCP
// tool server for the calendars and contacts Radicale already serves.
//
// dav-mcp serves streamable HTTP itself, so there is no bridge process in front
// of it: this app is one container that speaks MCP on one port and talks
// CalDAV/CardDAV out to Radicale. The wrapper is not a browser and cannot join
// the identity provider, so it authenticates to Radicale with the
// caldav-service account credential Bloud provisions and publishes under
// Radicale's `appApi` offer; the address comes from the `caldav` contract.
//
// The image reads its configuration from process environment variables and
// nothing else, so the resolved bindings reach it through a generated env file
// declared as the container's `envFile`. That is the same shape apps/affine-mcp
// uses to hand over a resolved base URL; the sink differs only because this
// image cannot read a config file.
package davmcp

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
	appName = "dav-mcp"
	// nodeName is the graph node and container name; see NodeLifecycle.Name.
	nodeName = "apps-dav-mcp"
	// defaultPort is the server's bind port and the host-published port. It
	// must match metadata.yaml's `port`. 9333 stays clear of the other
	// catalog apps.
	defaultPort = 9333

	// configDir is the subdirectory of the app data tree that holds the
	// generated env file, and envFileName the file itself. The directory is
	// not mounted into the container: the runtime reads the file on the host
	// and passes its values in at create time.
	configDir   = "config"
	envFileName = "env"

	// The environment variable names dav-mcp reads. Kept as constants so the
	// renderer and the tests name the same keys the upstream image expects.
	envServerURLKey = "CALDAV_SERVER_URL"
	envUsernameKey  = "CALDAV_USERNAME"
	envPasswordKey  = "CALDAV_PASSWORD"
	envBearerKey    = "BEARER_TOKEN"

	// httpTokenKey is the secret name the `mcp` contract carries. It must match
	// the contract registry and metadata.yaml's provides.mcp.secrets.
	httpTokenKey = "httpToken"
)

// Configurator handles the dav-mcp node lifecycle: write the env file the
// container is created from (PreStart) and verify the server is serving MCP
// (PostStart).
type Configurator struct {
	port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *mcpAPI

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:port.
	baseURL string
}

// NewConfigurator creates a new dav-mcp configurator from the host Deps.
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

// PreStart writes the env file the container spec declares as its envFile. It
// carries the three values `environment:` cannot render: the DAV address from the
// `caldav` binding, the caldav-service credential from the `appApi` binding,
// and the MCP bearer this app generates.
//
// The bearer is generated on the first pass and reused afterwards, so a restart
// does not invalidate the namespace a harness registered.
//
// A binding that has not resolved yet is written as absent rather than as an
// empty value, and a later pass restarts the container once it lands. That keeps
// "not ready" distinguishable from "configured with nothing", which the app
// would otherwise read as the latter and fail on.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	token, err := c.ensureHTTPToken()
	if err != nil {
		return configurator.NoRestart(), err
	}

	baseURL, hasAddress := caldavBaseURL(state)
	cred, hasCred := caldavCredential(state)
	if !hasAddress {
		c.logger.Warn("dav-mcp: no CalDAV address bound yet; writing a config without one")
	}
	if !hasCred {
		c.logger.Warn("dav-mcp: no published caldav-service credential yet; writing an unauthenticated config")
	}
	if token == "" {
		c.logger.Warn("dav-mcp: no MCP bearer available; the server will refuse every request")
	}

	content := renderEnvFile(baseURL, hasAddress, cred, hasCred, token)
	path := filepath.Join(state.DataPath, configDir, envFileName)

	changed, err := managedfile.Write(path, []byte(content), managedfile.ModeSharedConfig)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("writing %s config: %w", appName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}
	c.logger.Info("wrote dav-mcp config", "path", path,
		"address", hasAddress, "credential", hasCred, "bearer", token != "")
	return configurator.MustRestart("dav-mcp config rewritten"), nil
}

// PostStart verifies the server answers the MCP handshake. The container's own
// /health is not enough: it returns 200 as long as the process is up, whether
// or not the DAV side works, so a probe against it promotes a node that cannot
// serve a single MCP request. This POSTs a real `initialize` to the same path a
// harness calls and requires the server's own identity back.
//
// Because PostStart also runs on every resync pass, a server that stops serving
// after it converged is surfaced rather than left silently up.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.api.waitServing(ctx, c.currentBearer()); err != nil {
		return fmt.Errorf("waiting for the dav-mcp server: %w", err)
	}
	c.logger.Info("dav-mcp is serving MCP")
	return nil
}

// --- MCP bearer ---

// ensureHTTPToken returns the bearer this app validates, generating and
// publishing one on the first pass and reusing it afterwards.
func (c *Configurator) ensureHTTPToken() (string, error) {
	if c.secrets == nil {
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
	c.logger.Info("generated the dav-mcp MCP bearer")
	return token, nil
}

// currentBearer reads the MCP bearer this app publishes without generating one.
// PreStart owns generation; PostStart only has to present whatever is current,
// so a rotation landing between the two phases is still picked up.
func (c *Configurator) currentBearer() string {
	if c.secrets == nil {
		return ""
	}
	return c.secrets.GetAppSecret(appName, httpTokenKey)
}

// randomToken returns 32 bytes of entropy as URL-safe base64.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// --- bindings ---

// caldavBaseURL returns the DAV root a machine client dials: the container
// address plus the provider's declared path. The server-side fetch is the whole
// reason this is the container address rather than the browser-facing one.
func caldavBaseURL(state *configurator.AppState) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, b := range state.Integrations.CalDAVServers {
		if !b.Installed || b.BaseURL == "" {
			continue
		}
		return b.BaseURL + b.Path, true
	}
	return "", false
}

// caldavCredential returns the caldav-service account credential from the
// `appApi` binding. A binding missing its password or username is the
// provider's "not published yet" state, not a usable credential.
func caldavCredential(state *configurator.AppState) (configurator.AppAPIBinding, bool) {
	if state == nil {
		return configurator.AppAPIBinding{}, false
	}
	for _, b := range state.Integrations.AppAPIs {
		if !b.Installed || b.Username == "" || b.Password == "" {
			continue
		}
		return b, true
	}
	return configurator.AppAPIBinding{}, false
}

// --- config rendering ---

// renderEnvFile renders the env file the container is created from: one
// KEY=value line per setting, single-quoted. Only values that resolved are
// written, so an unresolved binding is an absent variable rather than an empty
// one, which the server reads differently.
//
// A value containing a quote or a line break is rejected rather than escaped.
// Every value here is generated (a base64url token) or resolved from catalog
// metadata (a container URL, an account name), so none should ever contain
// one; failing loudly beats writing a file whose quoting no longer means what it
// says.
func renderEnvFile(baseURL string, hasAddress bool, cred configurator.AppAPIBinding, hasCred bool, token string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud; rewritten on every reconciliation.\n")

	settings := make([][2]string, 0, 4)
	if hasAddress {
		settings = append(settings, [2]string{envServerURLKey, baseURL})
	}
	if hasCred {
		settings = append(settings, [2]string{envUsernameKey, cred.Username})
		settings = append(settings, [2]string{envPasswordKey, cred.Password})
	}
	if token != "" {
		settings = append(settings, [2]string{envBearerKey, token})
	}
	for _, kv := range settings {
		if strings.ContainsAny(kv[1], "'\r\n") {
			// Never echo the value: one of these keys is a password.
			fmt.Fprintf(&b, "# %s omitted: contains a quote or line break\n", kv[0])
			continue
		}
		fmt.Fprintf(&b, "%s='%s'\n", kv[0], kv[1])
	}
	return b.String()
}
