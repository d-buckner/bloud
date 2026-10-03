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
	// SetAppContractValue persists a non-secret value a provider mints at
	// runtime for one contract it provides, the counterpart of SetAppSecret for
	// the `values` half of a contract. The key must be one the provider's offer
	// lists under `runtimeValues`, which the catalog loader enforces; that is
	// what stops a configurator publishing an undeclared fact into someone
	// else's binding. Used where the value cannot exist in metadata because the
	// app has to produce it first (an endpoint path containing an id the app
	// minted on first boot).
	SetAppContractValue(appName, contract, key, value string) error
	// GetAppContractValue reads back a value published through
	// SetAppContractValue. Empty means the provider has not published it yet.
	GetAppContractValue(appName, contract, key string) string
}

// PreStartResult is what a configurator reports when its PreStart pass ends.
//
// The field is named for the side effect the orchestrator performs on it, not for
// what the configurator did to the filesystem. The two are different claims: a
// configurator can need a recreate without writing anything (a running instance
// that never picked up an earlier write), and a file write does not always mean
// the container has to be replaced. Keeping the recreate decision explicit is
// what stops each app reading the signal its own way.
type PreStartResult struct {
	// RestartNeeded is true when the container must be removed and created
	// again for reality to match intent. Report it when the running container
	// cannot pick up the change by itself; do not report it for work that only
	// touched directories, or for a file the app re-reads on its own.
	RestartNeeded bool
	// Reason is a short, human-readable clause saying why a restart is needed.
	// It goes to the orchestrator's log, so the recreate is traceable to the
	// signal that asked for it. Required when RestartNeeded is true; ignored
	// otherwise.
	Reason string
}

// NoRestart is the result of a pass that needs no container recreate.
func NoRestart() PreStartResult { return PreStartResult{} }

// MustRestart reports that the container has to be recreated, and why.
func MustRestart(reason string) PreStartResult {
	return PreStartResult{RestartNeeded: true, Reason: reason}
}

// RestartIf reports a recreate only when cond holds, so a configurator can
// thread a condition through without an if-block per signal.
func RestartIf(cond bool, reason string) PreStartResult {
	if cond {
		return MustRestart(reason)
	}
	return NoRestart()
}

// Or folds two independent signals together, keeping the first reason that
// asked for a recreate. A configurator that checks several things combines them
// with this so each signal keeps its own reason instead of being flattened into
// a boolean OR.
func (r PreStartResult) Or(other PreStartResult) PreStartResult {
	if !other.RestartNeeded {
		return r
	}
	if r.RestartNeeded {
		return r
	}
	return other
}

// NodeLifecycle handles the lifecycle of a single app node.
// All methods must be idempotent - safe to call repeatedly.
type NodeLifecycle interface {
	// Name returns the graph node this configurator manages. That node name is also
	// the container name the host-agent reconciles it under (apps-<app>), so an
	// app keeps it in one constant and reads it from both its registration key
	// and this method: the two cannot drift apart.
	Name() string

	// PreStart runs before the container starts.
	// Use for: config files, directories, certificates, initial setup.
	// Report RestartNeeded when the container must be recreated for reality to
	// match intent, with the Reason that explains it. See PreStartResult for
	// what does and does not qualify.
	PreStart(ctx context.Context, state *AppState) (PreStartResult, error)

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

// ProviderKind distinguishes what a ProviderRef points at. It is a wire type: a
// plain string so a consumer can compare it without importing anything, and the
// values mirror the catalog's `compatible:` discriminator (`app:` versus
// `source: instance`).
type ProviderKind string

const (
	// ProviderKindApp is an installed catalog app with containers and a node.
	ProviderKindApp ProviderKind = "app"
	// ProviderKindInstance is the instance's own configuration: no node, no
	// port, no container. Node, Port, BaseURL and LocalURL are empty; the
	// contract's own endpoint field carries the value.
	ProviderKindInstance ProviderKind = "instance"
)

// ProviderRef is the part of an integration binding that is the same for every
// contract: which app the provider is, whether it is installed, and where it is.
// The contract payloads below embed it, so a consumer reads the address and its
// role-specific fields from one value.
type ProviderRef struct {
	// Kind distinguishes an installed catalog app from the instance's own
	// configuration. It is the discriminator behind `compatible: [{app: ...}]`
	// versus `compatible: [{source: instance}]`.
	Kind ProviderKind
	// App is the provider's catalog ID, e.g. "sonarr". For an instance
	// provider it is catalog.InstanceProviderSource ("instance"), which is a
	// reserved value and never a real catalog ID.
	App string
	// Installed reports whether the provider is installed. It mirrors the
	// dependency edge the same provider gets in the graph, so it is true exactly
	// when the provider is wired to run before this app. A consumer that has an
	// entry to prune uses it to tell "wire to this provider" from "the provider
	// is gone"; the binding still carries the provider's address, from its
	// catalog metadata, which is what a prune needs to recognize the entry Bloud
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

// ModelSourceBinding is an OpenAI-compatible upstream that a gateway can route
// to. It is the provider side of the routing chain: the instance's external
// server, or a local runtime such as Ollama. Applications do not consume this
// contract directly; they consume InferenceBinding.
type ModelSourceBinding struct {
	ProviderRef
	// Endpoint is the OpenAI-compatible base URL as a client should pass it to
	// an SDK, path included: http://apps-ollama:11434/v1, or the operator's
	// external origin. Unlike ProviderRef.BaseURL it is not derived from a
	// container address, because the upstream may not be on any Bloud network.
	Endpoint string
	// APIKey is the credential to send upstream. Empty when the source needs
	// none (a local runtime on a trusted network).
	APIKey string
	// Models is the discovered model list, empty when discovery has not run or
	// the upstream does not offer /models.
	Models []string
}

// InferenceBinding is the OpenAI-compatible endpoint an application dials.
//
// The consumer declares `inference` and never learns what is behind it. That is
// the property the contract exists to hold: whether the endpoint is the operator's
// raw server or a gateway app sitting in front of several upstreams, the
// consumer's metadata, its config, and the operator's Settings entry stay
// unchanged.
type InferenceBinding struct {
	ProviderRef
	// Endpoint is the OpenAI-compatible base URL exactly as a client should
	// pass it to an SDK, path included: http://apps-gateway:4000/v1. This is
	// the field that always carries a usable value: ProviderRef.BaseURL is a
	// container-network fact (http://<Node>:<Port>) and is empty for an
	// instance provider, which has no container at all.
	Endpoint string
	// APIKey is the credential to send. Empty when the provider needs none.
	APIKey string
	// DefaultModel is the model to use where the app has picked none. It is a
	// concrete model id, never a tier or alias, because it has to resolve on a
	// server that has no Bloud-side name translation in front of it.
	DefaultModel string
	// Models is the model list on offer, for a consumer that renders a picker.
	Models []string
	// ViaGateway reports whether Endpoint is a Bloud-side gateway rather than
	// the raw upstream. A configurator uses it to tell a gateway credential
	// from the operator's real one.
	ViaGateway bool
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

// MCPBinding is a streamable-HTTP MCP server a harness registers as a tool
// namespace.
//
// It carries the address and the path separately, and no composed URL. That is
// deliberate: ProviderRef.BaseURL is a container-network name that only resolves
// for a consumer sharing the provider's network namespace, so a URL composed on
// the provider's side would be right for some consumers and silently wrong for
// others. The consumer picks the address its own topology can dial and appends
// Path.
type MCPBinding struct {
	ProviderRef
	// ServerName is the tool namespace the harness registers this server under.
	ServerName string
	// Token is the bearer the provider's MCP listener expects, published under
	// the provider's `mcp` contract. It is scoped to MCP: revoking it revokes
	// tool access and nothing else. Empty while the provider has not published it
	// yet, which a harness must treat as "not ready" and write no entry, never
	// as an empty bearer.
	Token string
	// Path is the endpoint path to append to whichever address the consumer
	// chose. Absolute, so composition is plain concatenation.
	Path string
}

// AppAPIBinding is a credential into the provider's own API, for a companion
// that is not a browser and cannot join the identity provider.
//
// It is the consumer-side half of the `appApi` contract: the target app mints
// the account credential and publishes it, and a wrapper consumes it. The
// binding carries the two fields a sign-in needs and nothing else. Both must be
// present before the consumer writes anything: an empty password means "not
// published yet", never an empty credential (the rule MCPBinding.Token follows
// for the same reason).
type AppAPIBinding struct {
	ProviderRef
	// Username is the account the credential belongs to, published as a
	// non-secret contract value. Empty until the provider publishes it.
	Username string
	// Password is the account password, published under the provider's `appApi`
	// contract. Empty while the provider has not published it, which a consumer
	// must treat as "not ready" and write no credential, never as an empty one.
	Password string
	// WorkspaceID is the scope the provider wants a companion to address by
	// default, published as a runtime value. AFFiNE's is the shared workspace
	// Bloud provisions. Empty when the provider has no scope or has not
	// published it yet, which a consumer reads as "not pinned" and leaves its
	// own default unset rather than writing an empty scope.
	WorkspaceID string
}

// CalDAVBinding is the DAV server a calendar or contacts client talks to.
//
// There is no credential field, and that is the contract, not a gap: a DAV
// server authenticates the person with the password they give the client, and
// the provider verifies it against the identity provider itself. Bloud does
// not route user passwords between apps, so a consumer of this contract gets
// the address and nothing else.
//
// It carries two addresses and no composed URL, for the same reason MCPBinding
// does. ProviderRef.BaseURL is the container-network address, which is the
// right one for a client running beside the provider on apps-net. PublicURL is
// the address a browser dials, and it is the only one that works for a
// browser-based client: the container name does not resolve there, and the
// browser will not send the user's credentials to an origin it did not get
// the page from. The consumer picks the one its own vantage point can use and
// appends Path.
type CalDAVBinding struct {
	ProviderRef
	// PublicURL is the origin the provider is reachable at from a browser:
	// the instance's public address with the provider's app subdomain on it.
	// Empty when the instance has no resolvable public address.
	PublicURL string
	// Path is the DAV root on whichever address the consumer chose. Absolute,
	// so composition is plain concatenation.
	Path string
}

// ICSFeedBinding is an ICS calendar feed a calendar server subscribes to on the
// user's behalf. The consumer fetches the feed server-side and projects the
// events into one of its own collections, so neither the feed URL nor its key
// ever reaches a client.
type ICSFeedBinding struct {
	ProviderRef
	// APIKey is the credential the provider's feed endpoint authenticates
	// with, published under the `icsFeed` contract. Empty while the provider
	// has not published it yet, which a consumer must treat as "not ready" and
	// write no sync job for, never as an empty key.
	APIKey string
	// Path is the feed path on the provider's address. Absolute, so the feed
	// URL is ProviderRef.BaseURL + Path with the key as a query parameter.
	Path string
	// DisplayName is what the consumer names the collection it creates for the
	// feed, e.g. "Radarr Movies".
	DisplayName string
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
	SSO             []SSOBinding
	ModelSources    []ModelSourceBinding
	Inference       []InferenceBinding
	MCPServers      []MCPBinding
	AppAPIs         []AppAPIBinding
	CalDAVServers   []CalDAVBinding
	ICSFeeds        []ICSFeedBinding
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
