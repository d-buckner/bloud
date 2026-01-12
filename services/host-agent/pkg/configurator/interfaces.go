package configurator

// RegistryInterface defines the interface for the configurator registry.
// This interface enables mocking for testing.
type RegistryInterface interface {
	// Get returns the configurator for an app, or nil if none exists.
	Get(appName string) Configurator

	// Has returns true if a configurator exists for the given app.
	Has(appName string) bool

	// All returns all registered configurators.
	All() []Configurator

	// Names returns the names of all registered configurators.
	Names() []string

	// Register adds a configurator to the registry.
	Register(c Configurator)
}

// Compile-time assertion
var _ RegistryInterface = (*Registry)(nil)
