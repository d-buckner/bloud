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

// Configurator handles app-specific configuration.
// All methods must be idempotent - safe to call repeatedly.
// Configurators run as systemd hooks on every service start:
// - PreStart runs as ExecStartPre (before container)
// - PostStart runs as ExecStartPost (after container healthy)
type Configurator interface {
	// Name returns the app name this configurator handles
	Name() string

	// PreStart runs before the container starts.
	// Use for: config files, directories, certificates, initial setup.
	// Called every reconciliation - must be idempotent.
	PreStart(ctx context.Context, state *AppState) error

	// HealthCheck waits for the app to be ready for configuration.
	// Use for: waiting for web UI, API, database to accept connections.
	// Returns nil when ready, error on timeout.
	HealthCheck(ctx context.Context) error

	// PostStart runs after container is healthy.
	// Use for: API calls, integrations, runtime configuration.
	// Called every reconciliation - must be idempotent.
	PostStart(ctx context.Context, state *AppState) error
}

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
