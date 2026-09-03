// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"log/slog"
	"sync"
)

// Registry manages NodeLifecycle implementations for all apps.
// System configurators register instances directly with Register. App
// configurators register factories via RegisterFactory (from their package
// init); those are instantiated lazily on first Get, so configurators for
// apps that are never installed are never constructed.
type Registry struct {
	configurators map[string]NodeLifecycle
	mu            sync.RWMutex
	logger        *slog.Logger
	deps          Deps
}

// NewRegistry creates a new configurator registry. deps are passed to app
// configurator factories when they are instantiated.
func NewRegistry(logger *slog.Logger, deps Deps) *Registry {
	return &Registry{
		configurators: make(map[string]NodeLifecycle),
		logger:        logger,
		deps:          deps,
	}
}

// Register adds a NodeLifecycle to the registry.
// If an implementation for the same app already exists, it will be replaced.
func (r *Registry) Register(c NodeLifecycle) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := c.Name()
	r.configurators[name] = c
	r.logger.Debug("registered configurator", "app", name)
}

// Get returns the NodeLifecycle for a node. If a factory was registered for
// the node but not yet instantiated, the factory runs now and the instance is
// cached. Returns nil when no configurator or factory exists, or when the
// factory fails (the error is logged).
func (r *Registry) Get(appName string) NodeLifecycle {
	r.mu.RLock()
	c := r.configurators[appName]
	r.mu.RUnlock()
	if c != nil {
		return c
	}

	factory, ok := lookupFactory(appName)
	if !ok {
		return nil
	}

	// Double-checked instantiation: only one goroutine builds the instance.
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.configurators[appName]; c != nil {
		return c
	}
	built, err := factory(r.deps)
	if err != nil {
		r.logger.Error("configurator factory failed", "app", appName, "error", err)
		return nil
	}
	if name := built.Name(); name != "" && appName != "" && name != appName {
		r.logger.Warn("factory-produced configurator name differs from registered name",
			"registered", appName, "configurator", name)
	}
	r.configurators[appName] = built
	r.logger.Debug("instantiated configurator", "app", appName)
	return built
}

// Has returns true if a NodeLifecycle exists (or a factory is registered)
// for the given app. It does not instantiate anything.
func (r *Registry) Has(appName string) bool {
	r.mu.RLock()
	_, ok := r.configurators[appName]
	r.mu.RUnlock()
	if ok {
		return true
	}
	_, ok = lookupFactory(appName)
	return ok
}

// All returns all instantiated NodeLifecycle implementations. Factory-
// registered apps are included only after their first Get.
func (r *Registry) All() []NodeLifecycle {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]NodeLifecycle, 0, len(r.configurators))
	for _, c := range r.configurators {
		result = append(result, c)
	}
	return result
}

// Names returns the names of all registered implementations, including
// factory-registered apps that have not been instantiated yet.
func (r *Registry) Names() []string {
	r.mu.RLock()
	seen := make(map[string]bool, len(r.configurators))
	result := make([]string, 0, len(r.configurators))
	for name := range r.configurators {
		seen[name] = true
		result = append(result, name)
	}
	r.mu.RUnlock()

	for _, name := range factoryNames() {
		if !seen[name] {
			result = append(result, name)
		}
	}
	return result
}
