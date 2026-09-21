// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
)

// Deps carries the host-side inputs app configurator factories need.
// It lives here (rather than an internal package) so app configurators in
// the separate apps module can accept it.
type Deps struct {
	// Logger for configurator logging.
	Logger *slog.Logger

	// Secrets provides app-specific generated secrets (may be nil in
	// degraded/CLI contexts; factories must tolerate nil).
	Secrets AppSecretsProvider

	// PrimaryBaseURL resolves the current primary host's public base URL.
	// It is a function so live host-set changes take effect without
	// re-registering configurators.
	PrimaryBaseURL func() string

	// TraefikPort is the public Traefik HTTP entrypoint port, used to build
	// host-local URLs (e.g. API calls made from the host itself).
	TraefikPort int

	// RestartContainer stops and starts a running container by name through
	// the host's container runtime, forcing its process to re-exec and re-read
	// on-disk config. Configurators need this when the app reads config only
	// at startup and its own in-app restart is unreliable (e.g. Home Assistant
	// under a container init). The runtime call is executed by the
	// orchestrator-provided callback so the orchestrator stays the single
	// writer of side effects. Nil when no runtime is available (CLI/tests);
	// factories that require a restart must treat nil as "cannot apply now".
	RestartContainer func(ctx context.Context, name string) error

	// Exec runs a command inside a running container and returns its combined
	// output. env entries are set in the container process, never on the command
	// line, so secrets do not leak into an argv. Like RestartContainer, the call
	// goes through the host runtime the orchestrator provides, so the
	// orchestrator stays the single writer of container side effects. Nil when
	// no runtime is available (CLI/tests); callers that require it must treat
	// nil as "cannot apply now".
	Exec ExecFunc

	// HTTP builds app HTTP clients for this process: shared transport,
	// default retry policy, shared logger. The zero value is usable (lazy
	// defaults), so a configurator can always call deps.HTTP.New(...).
	HTTP ClientFactory

	// Assets installs static/downloaded files with the shared content
	// cache. The zero value is usable. Populated by the host runtime; see
	// pkg/appasset.
	Assets appasset.Installer
}

// LocalTraefikURL returns the loopback URL of the Traefik HTTP entrypoint.
// Configurators running on the host call Traefik (and the services behind
// it) at this URL regardless of the public host set.
func (d Deps) LocalTraefikURL() string {
	return fmt.Sprintf("http://localhost:%d", d.TraefikPort)
}

// Factory constructs a NodeLifecycle for a specific app node.
// It returns an error when the configurator cannot be built with the given
// dependencies (e.g. a required dependency is missing in this mode).
type Factory func(deps Deps) (NodeLifecycle, error)

// ExecFunc runs a command inside a named container and returns its combined
// output. It is the shape of Deps.Exec (see there for the nil contract).
type ExecFunc func(ctx context.Context, containerName string, env map[string]string, cmd []string) ([]byte, error)

var (
	factoryMu     sync.RWMutex
	nodeFactories = map[string]Factory{}
)

// RegisterFactory registers a lazily-instantiated configurator factory for an
// app/node. App packages call this from their init() (see each app's
// registration.go), so adding an app never touches central wiring code.
// The configurator itself is constructed only on the first Registry.Get for
// that node. Re-registering a name replaces the previous factory.
func RegisterFactory(nodeName string, f Factory) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	nodeFactories[nodeName] = f
}

// lookupFactory returns the registered factory for a node, if any.
func lookupFactory(nodeName string) (Factory, bool) {
	factoryMu.RLock()
	defer factoryMu.RUnlock()
	f, ok := nodeFactories[nodeName]
	return f, ok
}

// factoryNames returns the names of all registered factories (for logging).
func factoryNames() []string {
	factoryMu.RLock()
	defer factoryMu.RUnlock()
	names := make([]string, 0, len(nodeFactories))
	for name := range nodeFactories {
		names = append(names, name)
	}
	return names
}

// MustRegisterFactory is a convenience for app packages: it registers a
// factory that always succeeds, hiding the error return.
func MustRegisterFactory(nodeName string, f func(deps Deps) NodeLifecycle) {
	RegisterFactory(nodeName, func(deps Deps) (NodeLifecycle, error) {
		c := f(deps)
		if c == nil {
			return nil, fmt.Errorf("configurator factory for %q returned nil", nodeName)
		}
		return c, nil
	})
}
