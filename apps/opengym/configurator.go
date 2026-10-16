// SPDX-License-Identifier: AGPL-3.0-only

// Package opengym configures openGym, the self-hosted gym and body-weight tracker.
//
// openGym keeps its own authentication: a profile is a passkey, and a passkey is
// bound to the exact hostname the browser is on. That makes the instance's public
// address part of the app's security configuration rather than a display detail, and
// it is the one thing about this install that a static manifest cannot state. So this
// package writes exactly that: the origin the app must recognise, re-rendered on every
// reconciliation and recreated into when it moves.
//
// Two nodes of the install get a configurator. The API node writes the address file its
// own container is created from; the web node proves the composed path a browser
// actually uses, because that is the half that breaks silently.
package opengym

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/managedfile"
)

const appName = "opengym"

const (
	// apiNodeName is the API container: the graph node that owns the address file.
	apiNodeName = "apps-opengym-api"
	// webNodeName is the nginx front end that publishes the app's port.
	webNodeName = "apps-opengym-web"
)

// defaultPort is the app's published port, matching metadata.yaml's `port`. Only the
// web container publishes it; the API listens on its own port inside the network.
const defaultPort = 8090

const (
	// configDir holds the host-only files this configurator writes. It is not
	// mounted: the runtime reads the env file on the host when it creates the
	// container, so the values never land in the container's own filesystem.
	configDir = "config"
	// envFileName is the API container's env file, declared in metadata.yaml as its
	// `envFile`.
	envFileName = "env"
)

// Configurator owns the apps-opengym-api node.
type Configurator struct {
	address func() string
	logger  *slog.Logger
}

// NewConfigurator builds the API node's configurator from host Deps.
func NewConfigurator(deps configurator.Deps) *Configurator {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		address: deps.Address.PrimaryBaseURL,
		logger:  logger.With("app", appName, "node", apiNodeName),
	}
}

// Name returns the graph node this configurator owns.
func (c *Configurator) Name() string { return apiNodeName }

// PreStart writes the two values that tie the app to the instance's address.
//
// It reports a recreate whenever the file changed, because openGym reads both at
// container create: a rewritten file beside a running container is a file that names
// one origin while the app refuses every other one. A steady-state pass writes
// identical bytes, `managedfile.Write` reports no change, and the pass touches
// nothing, which is what invariant 2 asks of a resync.
//
// The values come from the live address rather than from anything captured at
// construction, so a change made in Settings reaches the app on the next pass without
// a reinstall. That matters more here than in most apps: a passkey registered under
// one hostname never works under another, so an address change that did not reach this
// container turns every sign-in into "verification failed" until someone notices.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (configurator.PreStartResult, error) {
	if state == nil {
		return configurator.NoRestart(), fmt.Errorf("%s: no app state", appName)
	}

	dir := filepath.Join(state.DataPath, configDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return configurator.NoRestart(), fmt.Errorf("%s: create config dir: %w", appName, err)
	}

	origin := configurator.AppExternalURL(c.address, appName)
	rpID, err := relyingPartyID(origin)
	if err != nil {
		return configurator.NoRestart(), err
	}

	path := filepath.Join(dir, envFileName)
	changed, err := managedfile.Write(path, []byte(renderEnvFile(rpID, origin)), managedfile.ModeHostOnly)
	if err != nil {
		return configurator.NoRestart(), fmt.Errorf("%s: write %s: %w", appName, envFileName, err)
	}
	if !changed {
		return configurator.NoRestart(), nil
	}

	c.logger.Info("wrote the app address file, recreate required to pick it up",
		"path", path, "origin", origin, "rpID", rpID)
	return configurator.MustRestart("openGym reads RP_ID and ORIGIN at container create"), nil
}

// PostStart has nothing to verify. The node's health check is the API answering its own
// `/api/health`, which it does only after its data directory is writable, and every
// value this node configures is read at container create rather than asked for over
// the network. The composed path through the front end is the web node's to prove.
func (c *Configurator) PostStart(context.Context, *configurator.AppState) error { return nil }

// relyingPartyID is the hostname half of a WebAuthn relying party: the origin without
// its scheme or port, because a passkey is bound to a hostname and not to a port.
func relyingPartyID(origin string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("%s: cannot read a hostname from the instance address %q", appName, origin)
	}
	return parsed.Hostname(), nil
}

// renderEnvFile builds the API container's env file: one KEY=value per line, `#`
// comments ignored, values unquoted. Neither value can need quoting: an origin is an
// ASCII scheme, host, and optional port, and a hostname carries no space.
func renderEnvFile(rpID, origin string) string {
	var b strings.Builder
	b.WriteString("# Generated by Bloud. Do not edit: the next reconcile overwrites this.\n")
	// The hostname a passkey is bound to. openGym compares it against the origin the
	// browser is on and refuses a ceremony that does not match, so this has to be the
	// name a person types, not the container's address.
	fmt.Fprintf(&b, "RP_ID=%s\n", rpID)
	// The full origin, which is what the app checks a same-origin request against. On
	// a plain-http deployment this is also the only way the app can tell a real
	// visitor from another host, because plain http sends no Sec-Fetch-Site.
	fmt.Fprintf(&b, "ORIGIN=%s\n", origin)
	return b.String()
}
