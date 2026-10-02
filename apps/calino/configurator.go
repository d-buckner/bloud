// SPDX-License-Identifier: AGPL-3.0-only

// Package calino wires Calino, a browser-based CalDAV calendar, into Bloud.
//
// The configurator writes no configuration file, because there is none to
// write. Calino is a static bundle behind Caddy: it reads no environment
// variables and mounts no state, and everything it knows, including the CalDAV
// credentials the user hands it, lives in that user's browser. The app is
// configured by the person using it, not by the host.
//
// What is left for a configurator is the one thing metadata cannot assert:
// that the container came up serving the app rather than an error page.
package calino

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const appName = "calino"

// nodeName is this app's graph node and container name; see NodeLifecycle.Name.
const nodeName = "apps-calino"

// defaultPort is the host-side port the app is published on, the value the
// constructor uses when registration passes 0. It matches metadata.yaml's
// `port`. The container's own port is 8080, fixed by the Caddyfile baked into
// the image; only the host side is Bloud's to choose, and 8080 belongs to
// Traefik.
const defaultPort = 8180

type Configurator struct {
	port   int
	logger *slog.Logger
	app    *calinoAPI

	// baseURL is a test seam: when set, the client resolves to it instead of
	// localhost:port. Never used to build request URLs by hand.
	baseURL string
}

// NewConfigurator creates a Calino configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		port:   port,
		logger: logger.With("app", appName),
	}
	c.app = newAPI(deps.HTTP, func() string {
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

// PreStart has nothing to do, and an explicit no-op is the honest shape: the
// app takes no configuration from the host, so there is no file to converge and
// no restart to ask for. Every reconciliation pass returns the same answer,
// which is exactly what the idempotency contract wants.
func (c *Configurator) PreStart(_ context.Context, _ *configurator.AppState) (configurator.PreStartResult, error) {
	return configurator.NoRestart(), nil
}

// PostStart checks the one property that makes this app real: the container is
// serving the Calino bundle. A 200 whose body is a Caddy error page, or a 404
// because the document root moved, both leave a node that reports RUNNING while
// showing the user nothing.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	status, mounted, err := c.app.probeShell(ctx)
	if err != nil {
		// Liveness is the health check's job, and PostStart runs after it
		// passes, so a failure here is a race rather than a fault. Say so and
		// let the next reconciliation re-probe.
		c.logger.Warn("could not probe the Calino bundle", "error", err)
		return nil
	}
	switch {
	case status != http.StatusOK:
		return fmt.Errorf("calino answered GET %s with %d; the static bundle is not being served", indexPath, status)
	case !mounted:
		return fmt.Errorf("calino served %s without its app mount point (%s); the bundle is missing or Caddy's root is wrong",
			indexPath, bundleProbe)
	}

	c.reportDAVProvider(state)
	c.logger.Info("calino bundle verified", "status", status)
	return nil
}

// reportDAVProvider states the required CalDAV provider in the log, because it
// is the one piece of information the operator ends up needing and the only
// place it can be said: the address goes into the user's browser, not into a
// file Bloud owns, so nothing else ever writes it down.
//
// A warning when the provider is missing rather than an error, because an empty
// binding is also what a store read failure produces (buildIntegrations logs
// and returns empty). Parking a working container in a terminal ERROR state on
// a transient store error would be the worse failure.
func (c *Configurator) reportDAVProvider(state *configurator.AppState) {
	providers := state.Integrations.CalDAVServers
	if len(providers) == 0 || !providers[0].Installed {
		c.logger.Warn("Calino has no CalDAV provider bound; the app will open with nothing to connect to",
			"requiredContract", "caldav")
		return
	}
	provider := providers[0]
	address := provider.PublicURL
	if address == "" {
		address = provider.BaseURL
	}
	c.logger.Info("Calino is paired with a CalDAV server",
		"provider", provider.App,
		"address", address+provider.Path,
		"note", "users enter this in Calino's server field with their own Bloud password")
}
