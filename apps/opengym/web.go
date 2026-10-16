// SPDX-License-Identifier: AGPL-3.0-only

package opengym

import (
	"context"
	"fmt"
	"log/slog"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// WebConfigurator owns the apps-opengym-web node: the nginx front end that publishes
// the app's port, and therefore the container Traefik routes to.
//
// It writes no file. Its whole job is the one check this app cannot make on its own:
// that a request arriving from outside reaches the API through the proxy. The two
// containers each prove themselves healthy on their own port, and neither of those
// facts says anything about the path between them, which is the path every real
// request takes.
type WebConfigurator struct {
	port   int
	logger *slog.Logger
	api    *webAPI
}

// NewWebConfigurator builds the web node's configurator from host Deps.
func NewWebConfigurator(port int, deps configurator.Deps) *WebConfigurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &WebConfigurator{
		port:   port,
		logger: logger.With("app", appName, "node", webNodeName),
	}
	if deps.HTTP != nil {
		c.api = newWebAPI(deps.HTTP, func() string { return fmt.Sprintf("http://localhost:%d", c.port) })
	}
	return c
}

// Name returns the graph node this configurator owns.
func (c *WebConfigurator) Name() string { return webNodeName }

// Port is the app port this node was constructed with, which is the catalog `port:`
// when registration passed 0. The conformance harness compares it against
// metadata.yaml, because only this node routes traffic.
func (c *WebConfigurator) Port() int { return c.port }

// PreStart has nothing to do. The front end's configuration is static metadata plus one
// value the container reads about itself at start, so there is no file to write here
// and no reason to reach the network in a phase that must not.
func (c *WebConfigurator) PreStart(context.Context, *configurator.AppState) (configurator.PreStartResult, error) {
	return configurator.NoRestart(), nil
}

// PostStart proves the path a browser uses: the published port, through nginx, on to
// the API.
//
// This is where a wrong DNS resolver shows up. The image resolves the API name per
// request through the `resolver` directive, and the address podman puts its network
// DNS on depends on how many networks already exist, so the container reads it from
// its own resolv.conf at start. If that ever produces an address nothing answers, the
// symptom is not a refused connection: nginx waits for a resolver that is not there,
// every /api request hangs, and both containers report themselves healthy the whole
// time. Only a request from outside the pair sees it.
//
// Because PostStart also runs on every resync pass, a path that breaks later,
// including one that breaks because the API container moved, is surfaced rather than
// left silently up.
func (c *WebConfigurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if c.api == nil {
		c.logger.Warn("no HTTP client available; cannot prove the app's API path")
		return nil
	}
	if err := c.api.waitServing(ctx); err != nil {
		return fmt.Errorf("%s: waiting for the app's API through its front end: %w", appName, err)
	}
	c.logger.Info("the app's API is reachable through its front end")
	return nil
}
