package configurator

// RegistryInterface defines the interface for the node lifecycle registry.
// This interface enables mocking for testing.
type RegistryInterface interface {
	// Get returns the NodeLifecycle for an app, or nil if none exists.
	Get(appName string) NodeLifecycle

	// Has returns true if a NodeLifecycle exists for the given app.
	Has(appName string) bool

	// All returns all registered NodeLifecycle implementations.
	All() []NodeLifecycle

	// Names returns the names of all registered implementations.
	Names() []string

	// Register adds a NodeLifecycle to the registry.
	Register(c NodeLifecycle)
}

// Compile-time assertion
var _ RegistryInterface = (*Registry)(nil)
