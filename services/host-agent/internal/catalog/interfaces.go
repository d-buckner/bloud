package catalog

// CacheInterface defines the interface for catalog caching.
// This interface enables mocking for testing.
type CacheInterface interface {
	// Get returns a single app from the cache by name
	Get(name string) (*App, error)

	// GetAll returns all apps from the cache
	GetAll() ([]*App, error)

	// GetUserApps returns only user-facing apps (excluding infrastructure)
	GetUserApps() ([]*App, error)

	// IsSystemAppByName checks if an app name corresponds to a system app
	IsSystemAppByName(name string) bool

	// Refresh loads all apps from the catalog and updates the database cache
	Refresh(loader *Loader) error
}

// AppGraphInterface defines the interface for app dependency resolution.
// This interface enables mocking for testing.
type AppGraphInterface interface {
	// PlanInstall computes what happens when installing an app
	PlanInstall(appName string) (*InstallPlan, error)

	// PlanRemove computes what happens when removing an app
	PlanRemove(appName string) (*RemovePlan, error)

	// SetInstalled updates which apps are installed
	SetInstalled(installed []string)

	// IsInstalled checks if an app is installed
	IsInstalled(appName string) bool

	// FindDependents returns installed apps that integrate with the given app
	FindDependents(appName string) []ConfigTask

	// GetCompatibleApps returns compatible apps for an integration, split by installed status
	GetCompatibleApps(appName string, integrationName string) (installed []CompatibleApp, available []CompatibleApp)

	// GetApps returns all app definitions
	GetApps() map[string]*AppDefinition
}

// Compile-time assertions
var _ CacheInterface = (*Cache)(nil)
var _ AppGraphInterface = (*AppGraph)(nil)
