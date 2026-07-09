// Package configurator provides the interface and utilities for app configuration.
// Configurators handle app-specific setup that can't be expressed purely in Nix,
// such as config file generation and API-based configuration.
package configurator

import (
	"context"
)

// AppSecretsProvider provides access to app-specific secrets.
// Implemented by the secrets.Manager in internal/secrets; exposed here so
// app configurators (in the separate apps/ module) can accept it without
// importing an internal package.
type AppSecretsProvider interface {
	// GenerateAppAdminPassword returns an existing admin password for the app,
	// or generates and persists a new one if none exists yet.
	GenerateAppAdminPassword(appName string) (string, error)
	// GetAppSecret returns a specific secret for an app (e.g. "oauthClientSecret").
	GetAppSecret(appName, key string) string
}

// NodeLifecycle handles the full lifecycle of a single app node.
// All methods must be idempotent - safe to call repeatedly.
type NodeLifecycle interface {
	// Name returns the app name this configurator handles.
	Name() string

	// PreStart runs before the container starts.
	// Use for: config files, directories, certificates, initial setup.
	// Returns true if any managed output changed, signaling the container
	// should be restarted (EnsureContainer will be called with forceRestart=true).
	PreStart(ctx context.Context, state *AppState) (changed bool, err error)

	// EnsureContainer creates or recreates the app container.
	// forceRestart removes any existing container before re-creating.
	EnsureContainer(ctx context.Context, forceRestart bool) error

	// HealthCheck waits for the app to be ready for configuration.
	// Use for: waiting for web UI, API, database to accept connections.
	// Returns nil when ready, error on timeout.
	HealthCheck(ctx context.Context) error

	// PostStart runs after container is healthy.
	// Use for: API calls, integrations, runtime configuration.
	// Called every reconciliation - must be idempotent.
	PostStart(ctx context.Context, state *AppState) error

	// Remove tears down the app: stops the container and optionally removes
	// all persistent data. Must be idempotent.
	Remove(ctx context.Context, state *AppState, clearData bool) error
}

// Configurator is an alias for NodeLifecycle for backward compatibility.
// Deprecated: use NodeLifecycle directly.
type Configurator = NodeLifecycle

// LDAPOutput describes the LDAP provider endpoint available to configurators.
type LDAPOutput struct {
	Host         string
	Port         int
	BaseDN       string
	BindUser     string
	BindPassword string
}

// AppState contains the inputs currently consumed by app configurators.
type AppState struct {
	// DataPath is the app's data directory.
	DataPath string

	// BloudDataPath is the shared Bloud data directory.
	BloudDataPath string

	// SSOEnabled indicates that the app should configure its supported SSO strategy.
	SSOEnabled bool

	// LDAP is populated when the app's SSO strategy is "ldap" and an LDAP provider
	// is configured. Nil otherwise.
	LDAP *LDAPOutput
}
