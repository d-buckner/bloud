package store

// AppStoreInterface defines the interface for managing installed apps.
// This interface enables mocking for testing.
type AppStoreInterface interface {
	// GetAll returns all installed apps
	GetAll() ([]*InstalledApp, error)

	// GetByCatalogID returns an installed app by catalog ID
	GetByCatalogID(catalogID string) (*InstalledApp, error)

	// GetInstalledCatalogIDs returns just the catalog IDs of installed apps
	GetInstalledCatalogIDs() ([]string, error)

	// Install records a new app installation (or re-install)
	Install(catalogID, displayName, version string, integrationConfig map[string]string, opts *InstallOptions) error

	// UpdateStatus updates the status of an installed app
	UpdateStatus(catalogID, status string) error

	// EnsureSystemApp ensures a system app (managed by the host agent) is registered with running status
	EnsureSystemApp(catalogID, displayName string, port int) error

	// SetTailnetID updates the tailnet_id for an installed app
	SetTailnetID(catalogID, tailnetID string) error

	// UpdateIntegrationConfig updates the integration config for an app
	UpdateIntegrationConfig(catalogID string, config map[string]string) error

	// UpdateDisplayName updates the display name of an installed app
	UpdateDisplayName(catalogID, displayName string) error

	// Uninstall removes an app from the database
	Uninstall(catalogID string) error

	// IsInstalled checks if an app is installed
	IsInstalled(catalogID string) (bool, error)

	// SetOnChange sets a callback that fires when app state changes
	SetOnChange(fn func())
}

// Compile-time assertion that AppStore implements AppStoreInterface
var _ AppStoreInterface = (*AppStore)(nil)

// PreferencesStoreInterface defines the interface for managing user preferences.
type PreferencesStoreInterface interface {
	// HasUsers checks if any users exist
	HasUsers() (bool, error)

	// EnsureUser creates a user preferences row if it doesn't already exist
	EnsureUser(username string) error

	// GetLayout returns the user's layout as an array of grid elements
	GetLayout(username string) ([]GridElement, error)

	// SetLayout updates the user's layout
	SetLayout(username string, elements []GridElement) error
}

// Compile-time assertion that PreferencesStore implements PreferencesStoreInterface
var _ PreferencesStoreInterface = (*PreferencesStore)(nil)

// ShareStoreInterface defines the interface for managing shares.
type ShareStoreInterface interface {
	Create(share Share) error
	GetByID(id string) (*Share, error)
	List() ([]*Share, error)
	Revoke(id string) error
}

// Compile-time assertion that ShareStore implements ShareStoreInterface
var _ ShareStoreInterface = (*ShareStore)(nil)

// GuestStoreInterface defines the interface for managing guests.
type GuestStoreInterface interface {
	Create(guest Guest) error
	GetByID(id string) (*Guest, error)
	List() ([]*Guest, error)
	Delete(id string) error
}

// Compile-time assertion that GuestStore implements GuestStoreInterface
var _ GuestStoreInterface = (*GuestStore)(nil)

// TailnetStoreInterface defines the interface for managing tailnet connections.
type TailnetStoreInterface interface {
	Create(conn TailnetConnection) error
	GetByID(id string) (*TailnetConnection, error)
	GetActive() (*TailnetConnection, error)
	List() ([]*TailnetConnection, error)
	Delete(id string) error
}

// Compile-time assertion that TailnetStore implements TailnetStoreInterface
var _ TailnetStoreInterface = (*TailnetStore)(nil)

// RemoteAppStoreInterface defines the interface for managing remote apps.
type RemoteAppStoreInterface interface {
	Create(app RemoteApp) error
	GetByID(id string) (*RemoteApp, error)
	List() ([]*RemoteApp, error)
	SetCredential(id string, encryptedCred []byte) error
	SetStatus(id, status string) error
	Delete(id string) error
}

// Compile-time assertion that RemoteAppStore implements RemoteAppStoreInterface
var _ RemoteAppStoreInterface = (*RemoteAppStore)(nil)
