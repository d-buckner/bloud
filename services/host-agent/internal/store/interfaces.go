// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package store

// AppStoreInterface defines the interface for managing installed apps.
type AppStoreInterface interface {
	GetAll() ([]*InstalledApp, error)
	GetByCatalogID(catalogID string) (*InstalledApp, error)
	GetInstalledCatalogIDs() ([]string, error)
	Install(catalogID, displayName, version string, integrationConfig map[string]string, opts *InstallOptions) error
	UpdateStatus(catalogID, status string) error
	EnsureSystemApp(catalogID, displayName string, port int) error
	SetTailnetID(catalogID, tailnetID string) error
	UpdateIntegrationConfig(catalogID string, config map[string]string) error
	UpdateDisplayName(catalogID, displayName string) error
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
