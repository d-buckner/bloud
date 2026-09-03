// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"fmt"
	"log/slog"
	"sync"
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
