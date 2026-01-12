package configurator

import (
	"log/slog"
	"sync"
)

// Registry manages configurators for all apps.
// Configurators register themselves and can be looked up by app name.
type Registry struct {
	configurators map[string]Configurator
	mu            sync.RWMutex
	logger        *slog.Logger
}

// NewRegistry creates a new configurator registry.
func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		configurators: make(map[string]Configurator),
		logger:        logger,
	}
}

// Register adds a configurator to the registry.
// If a configurator for the same app already exists, it will be replaced.
func (r *Registry) Register(c Configurator) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := c.Name()
	r.configurators[name] = c
	r.logger.Debug("registered configurator", "app", name)
}

// Get returns the configurator for an app, or nil if none exists.
func (r *Registry) Get(appName string) Configurator {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.configurators[appName]
}

// Has returns true if a configurator exists for the given app.
func (r *Registry) Has(appName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.configurators[appName]
	return ok
}

// All returns all registered configurators.
func (r *Registry) All() []Configurator {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Configurator, 0, len(r.configurators))
	for _, c := range r.configurators {
		result = append(result, c)
	}
	return result
}

// Names returns the names of all registered configurators.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.configurators))
	for name := range r.configurators {
		result = append(result, name)
	}
	return result
}
