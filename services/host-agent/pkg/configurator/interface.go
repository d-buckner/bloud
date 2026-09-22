// SPDX-License-Identifier: AGPL-3.0-only

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
	// SetAppSecret persists a credential an app generated for itself, so a
	// consumer can be handed it through IntegrationBinding.Secrets instead of
	// reading the provider's files. The key must be one the app declares in
	// its `provides.secrets` catalog metadata, which is what makes the value
	// visible to consumers; anything else stays private to this app.
	SetAppSecret(appName, key, value string) error
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

// ProviderRef is the part of an integration binding that is the same for every
// contract: which app the provider is, whether it is installed, and where it is.
// The contract payloads below embed it, so a consumer reads the address and its
// role-specific fields from one value.
type ProviderRef struct {
	// App is the provider's catalog ID, e.g. "sonarr".
	App string
	// Installed reports whether the provider is installed. It mirrors the
	// dependency edge the same provider gets in the graph, so it is true exactly
	// when the provider is wired to run before this app. A consumer that has an
	// entry to prune uses it to tell "wire to this provider" from "the provider
	// is gone"; the binding still carries the provider's address, from its
	// catalog metadata, which is what a prune needs to recognise the entry Bloud
	// wrote.
	Installed bool
	// Node is the provider's primary graph node, which is also its container
	// name on the shared network, e.g. "apps-sonarr".
	Node string
	// Port is the provider's published port, from its catalog metadata.
	Port int
	// BaseURL is how a container of the consuming app reaches the provider:
	// http://<Node>:<Port>. It is what a consumer stores in its own
	// configuration (as an address the app itself connects to). Empty when the
	// provider publishes no port.
	BaseURL string
	// LocalURL reaches the same provider from the host:
	// http://localhost:<Port>. A configurator's own calls leave the app's
	// container, so they need this vantage point, while what the app stores
	// needs BaseURL. Empty when the provider publishes no port.
	LocalURL string
}

// PVRBinding is an app that holds recordings or a library and accepts requests
// or indexers over its own API: the consumer needs the key that API
// authenticates with.
type PVRBinding struct {
	ProviderRef
	// APIKey is the key the provider's API authenticates with, published by the
	// provider under its `pvr` contract. Empty while the provider has not
	// published it yet (treat that as "not ready", never as an empty
	// credential).
	APIKey string
}

// MediaServerBinding is an app that serves media and owns the accounts a
// consumer onboards against: the consumer needs the bootstrap admin password to
// log in.
type MediaServerBinding struct {
	ProviderRef
	// AdminPassword is the bootstrap admin password the host generated for the
	// provider, published under its `mediaServer` contract. Empty while it has
	// not been generated yet.
	AdminPassword string
}

// DownloadClientBinding is an app that fetches releases: the consumer stores its
// address in its own configuration and hands it work, so it needs nothing beyond
// the address.
type DownloadClientBinding struct {
	ProviderRef
}

// SSOBinding is the identity provider an app authenticates through, with the API
// token needed to read its users.
type SSOBinding struct {
	ProviderRef
	// APIToken is the token the provider generated for the host and published
	// under its `sso` contract. Empty while it has not been published yet.
	APIToken string
}

// MCPBinding is a Model Context Protocol server an agent app registers.
type MCPBinding struct {
	ProviderRef
	// ServerName is the provider's own name for the server, e.g. "affine".
	ServerName string
	// URL is the endpoint as the consuming app's containers reach it:
	// http://<Node>:<Port><path>.
	URL string
	// Token is the bearer token the provider's listener expects, published under
	// its `mcp` contract. Empty while it has not been published yet.
	Token string
}

// Integrations holds the resolved providers for every contract the app declares
// in its catalog metadata, one typed slice per contract.
//
// The shape is deliberate: each contract carries only what its consumers read,
// a provider of an existing contract needs no code change, and no consumer has
// to pick its fields out of a shared struct (or nil-check the contracts it does
// not use). A contract whose binding needs a new field is a payload type change,
// not a change to every consumer.
type Integrations struct {
	PVRs            []PVRBinding
	MediaServers    []MediaServerBinding
	DownloadClients []DownloadClientBinding
	MCPServers      []MCPBinding
	SSO             []SSOBinding
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

	// Integrations holds the resolved providers for every contract the app
	// declares in its catalog metadata, one typed slice per contract. A contract
	// with no provider is an empty slice, and a `multi` contract can hold one
	// binding per provider.
	Integrations Integrations
}
