// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package configurator provides the interface and utilities for app configuration.
// Configurators handle app-specific setup that can't be expressed in static
// config (manifests / container defs): config-file generation, API-based setup.
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

// NodeLifecycle handles the lifecycle of a single app node.
// All methods must be idempotent - safe to call repeatedly.
type NodeLifecycle interface {
	// Name returns the app name this configurator handles.
	Name() string

	// PreStart runs before the container starts.
	// Use for: config files, directories, certificates, initial setup.
	// Returns changed=true when mounted file contents were modified, signalling
	// that the container must be restarted to pick up the new configuration.
	PreStart(ctx context.Context, state *AppState) (changed bool, err error)

	// PostStart runs after container is healthy.
	// Use for: API calls, integrations, runtime configuration.
	// Called every reconciliation - must be idempotent.
	//
	// Error contract: PostStart has no retry of its own. The orchestrator treats
	// any returned error as terminal for the node (it lands in ERROR until a new
	// install intent re-drives it), so a configurator must resolve transient
	// conditions itself - wait for readiness or tolerate the failure - and
	// return an error only for a genuine, persistent fault. A best-effort check
	// that cannot be proven now should log and return nil; the next pass
	// re-checks it.
	PostStart(ctx context.Context, state *AppState) error
}

// Remover is implemented by configurators that own teardown of their node. It is
// optional and separate from NodeLifecycle: the orchestrator deletes containers
// and app data itself, so only configurators with teardown the orchestrator
// cannot express (a direct container-runtime call, for example) implement it.
//
// Must be idempotent.
type Remover interface {
	// Remove tears down the app and optionally removes all persistent data.
	Remove(ctx context.Context, state *AppState, clearData bool) error
}

// Configurator is an alias for NodeLifecycle for backward compatibility.
type Configurator = NodeLifecycle

// LDAPOutput describes the LDAP provider endpoint available to configurators.
type LDAPOutput struct {
	Host         string
	Port         int
	BaseDN       string
	BindUser     string
	BindPassword string
}

// OIDCOutput describes the native OIDC provider the host-agent provisions in
// the identity provider for an app. Populated when the app's SSO strategy is
// "native-oidc"; nil otherwise.
type OIDCOutput struct {
	ClientID     string
	ClientSecret string
	IssuerURL    string // Discovery endpoint, e.g. http://localhost:8080/application/o/immich/
	RedirectURI  string // Primary redirect URI registered with the provider
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

	// OIDC is populated when the app's SSO strategy is "native-oidc" and an OIDC
	// provider is configured. Nil otherwise.
	OIDC *OIDCOutput
}
