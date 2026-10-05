// SPDX-License-Identifier: AGPL-3.0-only

package store

// AppStoreInterface defines the interface for managing installed apps.
type AppStoreInterface interface {
	GetAll() ([]*InstalledApp, error)
	GetByCatalogID(catalogID string) (*InstalledApp, error)
	GetInstalledCatalogIDs() ([]string, error)
	Install(catalogID, displayName, version string, integrationConfig map[string]string, opts *InstallOptions) error
	UpdateStatus(catalogID string, status AppStatus) error
	SetLastError(catalogID, lastError string) error
	EnsureSystemApp(catalogID, displayName string, port int) error
	UpdateIntegrationConfig(catalogID string, config map[string]string) error
	UpdateDisplayName(catalogID, displayName string) error
	GetSSOStrategy(catalogID string) (string, error)
	SetSSOStrategy(catalogID, strategy string) error
	Uninstall(catalogID string) error
	IsInstalled(catalogID string) (bool, error)
	SetOnChange(fn func())
}

// Compile-time assertion that AppStore implements AppStoreInterface
var _ AppStoreInterface = (*AppStore)(nil)

// PreferencesStoreInterface defines the interface for managing user preferences.
type PreferencesStoreInterface interface {
	HasUsers() (bool, error)
	EnsureUser(username string) error
	DeleteUser(username string) error
}

// Compile-time assertion that PreferencesStore implements PreferencesStoreInterface
var _ PreferencesStoreInterface = (*PreferencesStore)(nil)

// PositionStoreInterface defines the interface for managing user grid positions.
type PositionStoreInterface interface {
	GetForUser(username string) ([]Position, error)
	SetForUser(username string, positions []Position) error
}

// Compile-time assertion that PositionStore implements PositionStoreInterface
var _ PositionStoreInterface = (*PositionStore)(nil)

// SessionStoreInterface defines the interface for managing user sessions.
type SessionStoreInterface interface {
	Create(userID, username string, role Role) (*Session, error)
	Get(sessionID string) (*Session, error)
	Delete(sessionID string) error
	DeleteByUserID(userID string) error
	DeleteByUsername(username string) error
	Refresh(sessionID string) error
	PurgeExpired() (int64, error)
}

// Compile-time assertion that SessionStore implements SessionStoreInterface
var _ SessionStoreInterface = (*SessionStore)(nil)
