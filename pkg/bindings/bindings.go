// SPDX-License-Identifier: AGPL-3.0-only

// Package bindings holds the integration-contract vocabulary a consumer
// reads: ProviderRef, ProviderKind, the per-contract binding payloads, and
// the Integrations set the orchestrator resolves. It is a sibling of the
// configurator framework package, split out so an app that imports
// configurator for the author contract does not also name every contract
// payload.
package bindings

// Secret is a credential a provider publishes to a consumer through a contract.
//
// An empty Secret means "the provider has not published this credential yet".
// It never means "an empty credential". A consumer must treat an unpublished
// Secret as not-ready and write nothing. That distinction is load-bearing: a
// config file that names an empty key lets the app start, authenticate with
// nothing, and fail somewhere the operator cannot see.
//
// The rule lives here once. Before it was a type, each binding field restated it
// in its own comment, and a paraphrase drifts from its neighbours.
//
// A provider publishes a credential through the host store
// (AppSecretsProvider.SetAppSecret). The orchestrator resolves it into a binding
// only when the consumer declared that secret under
// `integrations.<contract>.requires`.
type Secret string

// Published reports whether the provider has published the credential. A
// consumer checks this before it writes anything that carries the value.
func (s Secret) Published() bool { return s != "" }

// String returns the credential as a plain string, for a call site that passes
// it to a string-typed API: an HTTP header, a config template, a JSON field.
func (s Secret) String() string { return string(s) }

// ProviderKind distinguishes what a ProviderRef points at. It is a wire type: a
// plain string so a consumer can compare it without importing anything, and its
// values name the three ways a provider can come into existence: a local catalog
// app, a remote one the operator registered, and a bare operator-declared
// setting with no app behind it.
type ProviderKind string

const (
	// ProviderKindApp is an installed catalog app with containers and a node.
	ProviderKindApp ProviderKind = "app"
	// ProviderKindSetting is an operator-declared value with no catalog app
	// behind it: no node, no port, no container. Node, Port, BaseURL and
	// LocalURL are empty; the contract's own endpoint field carries the value.
	//
	// This is the old ProviderKindInstance. "Instance" was honest only while
	// there was exactly one such thing, the AI Settings. Once a declared
	// contract can name any provider source, the accurate word is "setting".
	// The value changes with the name because nothing outside this package
	// reads it as a string; the separate `source: setting` metadata value and
	// the reserved catalog.SettingProviderSource are a catalog-compatibility
	// question and are untouched here.
	ProviderKindSetting ProviderKind = "setting"
	// ProviderKindExternalApp is a remote install of a catalog app: the
	// operator registered the app as running somewhere Bloud does not run it.
	// It keeps the catalog ID in App and satisfies the same contracts the
	// catalog entry declares, but it has no node, no port, and no container.
	// BaseURL and LocalURL both carry the operator's endpoint, because there
	// is no container-network vantage point distinct from the host's: what the
	// consumer's app stores and what its configurator dials are the same
	// origin.
	ProviderKindExternalApp ProviderKind = "externalApp"
)

// ProviderRef is the part of an integration binding that is the same for every
// contract: which app the provider is, whether it is installed, and where it is.
// The contract payloads below embed it, so a consumer reads the address and its
// role-specific fields from one value.
type ProviderRef struct {
	// Kind distinguishes an installed catalog app from a remote one and from an
	// operator-declared setting. It is the discriminator behind
	// `compatible: [{app: ...}]` versus `compatible: [{source: setting}]`.
	//
	// A consumer must not branch on it. Every field below carries a usable
	// value for a local app and for an external one, which is the whole point
	// of the abstraction: where the provider lives is not the consumer's
	// business.
	Kind ProviderKind
	// App is the provider's catalog ID, e.g. "sonarr". For a setting provider
	// it is catalog.SettingProviderSource ("setting"), which is a reserved
	// value and never a real catalog ID.
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
	// APIKey is the credential to send upstream. A local runtime on a trusted
	// network needs none, so this one may legitimately stay unpublished.
	APIKey Secret
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
	// APIKey is the credential to send. A provider that needs none leaves it
	// unpublished.
	APIKey Secret
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

// TranscriptionBinding is a speech-to-text endpoint a consumer can POST audio
// to: an OpenAI-compatible `/v1/audio/transcriptions`.
//
// It is its own binding rather than two more fields on InferenceBinding because
// the two promise different things. An inference upstream is a server that
// answers completions; a transcription endpoint is a server that answers audio
// uploads, and the same box can do one and not the other. A consumer that asked
// for this binding has been told, by the operator's own declaration, that the
// address transcribes.
type TranscriptionBinding struct {
	ProviderRef
	// Endpoint is the base URL an SDK should be constructed with, path
	// included, for example https://api.openai.com/v1. As with inference, this
	// is the field that always carries something dialable; ProviderRef.BaseURL
	// is a container-network fact and is empty for an instance provider.
	Endpoint string
	// APIKey is the bearer to send. An endpoint that needs none leaves it
	// unpublished.
	APIKey Secret
	// Model is the model id to request, empty when the operator expressed no
	// preference and the endpoint's own default stands. It is a concrete id
	// rather than a tier for the same reason inference's DefaultModel is: the
	// server behind this address has no Bloud-side name translation in front
	// of it.
	Model string
}

// PVRBinding is an app that holds recordings or a library and accepts requests
// or indexers over its own API: the consumer needs the key that API
// authenticates with.
type PVRBinding struct {
	ProviderRef
	// APIKey is the key the provider's API authenticates with, published by the
	// provider under its `pvr` contract.
	APIKey Secret
}

// RequestManagerBinding is the app an agent asks to take requests on the
// user's behalf. The consumer needs the credentials the provider's own login
// is built on (for Seerr, the Jellyfin account its admin was created from) so
// it can derive the key the API authenticates with.
type RequestManagerBinding struct {
	ProviderRef
	// Username is the login paired with Password: for Seerr, the Jellyfin
	// account, which is also the display name the manager reports and the
	// account requests are attributed to. A Bloud-booted provider publishes the
	// account it onboarded with; an off-host provider publishes the one its
	// operator typed in.
	Username string
	// Password is the credential paired with Username, published under the
	// provider's `requestManager` contract.
	Password Secret
}

// MediaServerBinding is an app that serves media and owns the accounts a
// consumer onboards against: the consumer needs the bootstrap admin password to
// log in.
//
// How the consumer spends that login is its own business. Most trade it for a
// long-lived API key of their own and cache that. A consumer that reaches the
// provider over the Subsonic protocol has no key to mint, so it signs every
// request with this account and holds nothing it could revoke.
type MediaServerBinding struct {
	ProviderRef
	// AdminUsername is the account the paired AdminPassword belongs to. A
	// Bloud-booted provider publishes the managed bootstrap account it made;
	// a provider the operator registered from off-host publishes the name they
	// typed in, because that is the only way anyone downstream can learn it.
	AdminUsername string
	// AdminPassword is the bootstrap admin password the host generated for the
	// provider, published under its `mediaServer` contract. Empty means the
	// provider has not published one yet, which a consumer reads as "not ready",
	// never as "no password".
	AdminPassword Secret
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
	// under its `sso` contract.
	APIToken Secret
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
	// tool access and nothing else.
	Token Secret
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
// present before the consumer writes anything.
type AppAPIBinding struct {
	ProviderRef
	// Username is the account the credential belongs to, published as a
	// non-secret contract value. Empty until the provider publishes it.
	Username string
	// Password is the account password, published under the provider's
	// `appApi` contract.
	Password Secret
	// WorkspaceID is the scope the provider wants a companion to address by
	// default, published as a runtime value. AFFiNE's is the shared workspace
	// Bloud provisions. Empty when the provider has no scope or has not
	// published it yet, which a consumer reads as "not pinned" and leaves its
	// own default unset rather than writing an empty scope.
	WorkspaceID string
}

// AgentAPIBinding is an agent endpoint a consumer dials: an OpenAI-compatible
// API whose far side is an agent rather than a model.
//
// It is the consumer-side half of the `agentApi` contract. The binding carries
// one composed address rather than a base the consumer finishes itself, and
// that is deliberate. The provider may be reachable from the consumer's network
// position by one address and not another: a host-networked agent is not
// resolvable by container name from the app network, so the only origin a
// consumer can actually dial is the one routed through the proxy. Handing over
// `BaseURL` and a path and letting the consumer compose would have it build the
// one address guaranteed not to resolve.
//
// Endpoint is therefore the whole thing, path included, exactly as a client
// passes it to an OpenAI SDK. A consumer that ignores it and composes from
// ProviderRef is a bug in the consumer, and the field's existence is what
// makes that checkable.
type AgentAPIBinding struct {
	ProviderRef
	// Endpoint is the agent's OpenAI-compatible base URL, path included:
	// http://hermes.<base>:8080/v1. Empty while the provider is not wired
	// or the instance has no address to route it on, which a consumer must
	// treat as "not ready" and write nothing.
	Endpoint string
	// APIKey is the bearer the provider's agent listener expects, published
	// under the provider's `agentApi` contract.
	APIKey Secret
	// ModelName is the model id the provider wants a consumer to request.
	// An agent answers to a name of its own choosing rather than a model id
	// the operator picked, so this is what a consumer sends where an OpenAI
	// client would put `gpt-4`. Empty means the provider has no preference
	// and the consumer's own default stands.
	ModelName string
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
	// with, published under the `icsFeed` contract.
	APIKey Secret
	// Path is the feed path on the provider's address. Absolute, so the feed
	// URL is ProviderRef.BaseURL + Path with the key as a query parameter.
	Path string
	// CalendarName is the collection the consumer creates for the feed. It
	// serves as both the collection's path segment and the display name a
	// calendar client shows, which is why one declaration covers both and the
	// two cannot be edited apart. Path-segment-safe by contract, so it can be
	// concatenated into a location rather than only used as a label.
	CalendarName string
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
	RequestManagers []RequestManagerBinding
	MediaServers    []MediaServerBinding
	DownloadClients []DownloadClientBinding
	SSO             []SSOBinding
	ModelSources    []ModelSourceBinding
	Inference       []InferenceBinding
	Transcription   []TranscriptionBinding
	MCPServers      []MCPBinding
	AppAPIs         []AppAPIBinding
	AgentAPIs       []AgentAPIBinding
	CalDAVServers   []CalDAVBinding
	ICSFeeds        []ICSFeedBinding
}
