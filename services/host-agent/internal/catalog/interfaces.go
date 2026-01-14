package catalog

// CacheInterface defines the interface for app catalog caching.
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

	// Refresh loads all apps from the catalog and updates the cache
	Refresh(loader *Loader) error
}

// Compile-time assertion that Cache implements CacheInterface
var _ CacheInterface = (*Cache)(nil)

// AppGraphInterface defines the interface for app dependency graph operations.
// This interface enables mocking for testing.
type AppGraphInterface interface {
	// PlanInstall returns an install plan for an app
	PlanInstall(appName string) (*InstallPlan, error)

	// PlanRemove returns a remove plan for an app
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

// Compile-time assertion that AppGraph implements AppGraphInterface
var _ AppGraphInterface = (*AppGraph)(nil)
