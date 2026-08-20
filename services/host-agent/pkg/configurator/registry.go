// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package configurator

import (
	"log/slog"
	"sync"
)

// Registry manages NodeLifecycle implementations for all apps.
// Implementations register themselves and can be looked up by app name.
type Registry struct {
	configurators map[string]NodeLifecycle
	mu            sync.RWMutex
	logger        *slog.Logger
}

// NewRegistry creates a new configurator registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		configurators: make(map[string]NodeLifecycle),
		logger:        logger,
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

// Get returns the NodeLifecycle for an app, or nil if none exists.
func (r *Registry) Get(appName string) NodeLifecycle {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.configurators[appName]
}

// Has returns true if a NodeLifecycle exists for the given app.
func (r *Registry) Has(appName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.configurators[appName]
	return ok
}

// All returns all registered NodeLifecycle implementations.
func (r *Registry) All() []NodeLifecycle {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]NodeLifecycle, 0, len(r.configurators))
	for _, c := range r.configurators {
		result = append(result, c)
	}
	return result
}

// Names returns the names of all registered implementations.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.configurators))
	for name := range r.configurators {
		result = append(result, name)
	}
	return result
}
