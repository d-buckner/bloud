package store

// AppStoreInterface defines the interface for managing installed apps.
// This interface enables mocking for testing.
type AppStoreInterface interface {
	// GetAll returns all installed apps
	GetAll() ([]*InstalledApp, error)

	// GetByName returns an installed app by name
	GetByName(name string) (*InstalledApp, error)

	// GetInstalledNames returns just the names of installed apps
	GetInstalledNames() ([]string, error)

	// Install records a new app installation (or re-install)
	Install(name, displayName, version string, integrationConfig map[string]string, opts *InstallOptions) error

	// UpdateStatus updates the status of an installed app
	UpdateStatus(name, status string) error

	// EnsureSystemApp ensures a system app (managed by NixOS) is registered with running status
	EnsureSystemApp(name, displayName string, port int) error

	// UpdateIntegrationConfig updates the integration config for an app
	UpdateIntegrationConfig(name string, config map[string]string) error

	// UpdateDisplayName updates the display name of an installed app
	UpdateDisplayName(name, displayName string) error

	// Uninstall removes an app from the database
	Uninstall(name string) error

	// IsInstalled checks if an app is installed
	IsInstalled(name string) (bool, error)

	// SetOnChange sets a callback that fires when app state changes
	SetOnChange(fn func())
}

// Compile-time assertion that AppStore implements AppStoreInterface
var _ AppStoreInterface = (*AppStore)(nil)
