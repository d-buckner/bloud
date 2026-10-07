// SPDX-License-Identifier: AGPL-3.0-only

package catalog

// A Contract is one integration contract: the label a consumer declares in its
// `integrations:` metadata, and what a provider must offer to satisfy it.
//
// This is the single place the vocabulary lives. A provider declares its offer
// under the contract name (metadata.yaml `provides: <contract>: ...`), a
// consumer declares the contract it wants, and both sides are validated against
// the entry here: the loader rejects a provider whose declaration does not match
// (a missing secret, a value that cannot be used) and the orchestrator reads the
// required secret names from it, so the code that builds a consumer's binding
// never repeats a name the metadata already agreed on.
//
// Adding a contract means adding an entry here, a payload type in
// pkg/configurator, and one arm in the orchestrator's resolver. A provider of an
// *existing* contract needs no code at all: it declares the contract in its
// metadata and the resolver builds the payload from it.
type Contract struct {
	// Name is the label appearing in a consumer's `integrations:` and in a
	// provider's `provides:`, e.g. "pvr".
	Name string
	// Secrets lists the credentials a provider must publish for this contract.
	// The orchestrator hands them to the consumer in the order they appear here,
	// so a single-secret contract needs no name on the consumer side either.
	Secrets []string
	// Values lists the non-secret values a provider must declare.
	Values []ValueSpec
	// SatisfiedBy names other contracts that can stand in for this one when no
	// provider of it is installed. The resolver falls back to these, in order,
	// and only when the contract itself has no installed provider: a real
	// provider always wins, so installing a gateway retires the fallback rather
	// than competing with it.
	//
	// The rule lives in the registry rather than in the resolver so the person
	// reading a consumer's metadata can see why an unmet contract still
	// resolves, instead of finding the exception buried in Go.
	SatisfiedBy []string
}

// ValueSpec is one non-secret value a contract requires of a provider.
type ValueSpec struct {
	// Key is the value's name in the provider's `provides: <contract>: values:`.
	Key string
	// AbsolutePath requires an absolute path, for a value that is concatenated
	// onto an address (a consumer would otherwise receive an unusable URL).
	AbsolutePath bool
	// Optional marks a value a provider may omit because it simply has no such
	// fact to declare (a companion's target with no scope, for example). An
	// omitted optional value reads as an empty field on the binding, never as
	// an error.
	Optional bool
	// PathSegment requires a value that is safe to use verbatim as one URL
	// path segment: no separator, no whitespace, no traversal. Set it on a
	// value the consumer concatenates into a *location* rather than reads as
	// a label, because that is where a declared `../..` stops being a typo and
	// becomes a way out of the tree it was meant to live in.
	PathSegment bool
}

// contracts is the integration vocabulary. Order is not significant.
var contracts = []Contract{
	// Address and reachability only: the consumer reaches the provider's own
	// API, as Sonarr and Radarr do with qBittorrent, or Traefik does with every
	// app it routes.
	{Name: "proxy"},
	{Name: "downloadClient"},
	{Name: "database"},

	// A PVR hands its consumer the key its own API authenticates with (Prowlarr
	// pushes indexers into it, Seerr hands it requests).
	{Name: "pvr", Secrets: []string{"apiKey"}},

	// An ICS calendar feed a calendar server subscribes to on the user's
	// behalf. The provider hands over the path to its feed and the key that
	// authenticates it; the consumer (Radicale's ics-sync storage plugin)
	// fetches the feed server-side and projects the events into one of its own
	// collections, so the user adds one CalDAV account and inherits every feed
	// the instance publishes.
	//
	// `apiKey` is the provider's own API key, the same credential its `pvr`
	// offer publishes. The Servarr feed endpoint authenticates with
	// `?apikey=`, which is the only form that works here: a calendar client,
	// and the storage plugin that stands in for one, cannot set an X-Api-Key
	// header.
	//
	// `calendarName` is the collection the consumer creates for the feed, and
	// one declaration covers both halves of it: the collection's path segment
	// and the display name a calendar client shows. Naming them separately is
	// how the two get edited apart.
	//
	// It is a provider fact and not something the consumer may derive from the
	// app id. Naming the collection after the app is what produced
	// `calendar-service/radarr` in a family member's calendar: the internal
	// service account leaking into the thing a person actually reads.
	{
		Name:    "icsFeed",
		Secrets: []string{"apiKey"},
		Values: []ValueSpec{
			{Key: "path", AbsolutePath: true},
			{Key: "calendarName", PathSegment: true},
		},
	},

	// A media server hands the consumer the bootstrap admin password it was
	// given, so the consumer can log in and mint its own key.
	{Name: "mediaServer", Secrets: []string{"adminPassword"}},

	// The identity provider hands the consumer the API token it generated for
	// the host, so an app can mirror its users.
	{Name: "sso", Secrets: []string{"apiToken"}},

	// An OpenAI-compatible upstream that something else can route to: the
	// operator's own server (provided by the instance, through Settings) or a
	// local model runtime (provided by an app such as Ollama). A gateway
	// consumes this; an application does not.
	//
	// The contract carries no secret. A local runtime on a trusted network has
	// no credential to publish, and making the secret mandatory would force
	// Ollama to invent one. The instance's credential is resolved by the
	// resolver from the secrets manager under the instance scope, not through
	// this contract, so the binding's APIKey is empty for a keyless provider
	// and populated for the operator's external server.
	{
		Name:   "modelSource",
		Values: []ValueSpec{{Key: "path", AbsolutePath: true}},
	},

	// The endpoint an application dials: base URL, a key, a default model.
	// Provided by a gateway app, and by promotion from any modelSource when no
	// gateway is installed. That promotion is what lets a consumer's metadata
	// stay identical whether it reaches a raw upstream or a gateway: adding the
	// gateway later changes nothing on the consumer side.
	//
	// Only a gateway provides this contract, and a gateway always has a
	// credential, so the secret is mandatory here even though its modelSource
	// fallback is keyless. A promoted binding from a keyless provider simply
	// carries an empty APIKey, which is the correct answer for a local runtime.
	{
		Name:        "inference",
		Secrets:     []string{"apiKey"},
		Values:      []ValueSpec{{Key: "path", AbsolutePath: true}},
		SatisfiedBy: []string{"modelSource"},
	},

	// An agent endpoint: an OpenAI-compatible API whose far side is not a
	// model but an agent, which answers in the same wire format and then goes
	// and does things. Provided by an app that runs one (Hermes' gateway),
	// consumed by anything that wants to route work to that agent instead of
	// to a bare completion endpoint.
	//
	// This is a separate contract from `inference` on purpose, and the reason
	// is what a consumer is agreeing to when it dials. `inference` means "a
	// model I can run completions against": harmless to point a summarizer
	// at, and a consumer has no reason to ask what is behind it. `agentApi`
	// means "a thing with tools, memory, and often a terminal, that happens
	// to speak OpenAI". Folding the two together would let a consumer that
	// asked for a model be handed an agent that can execute commands, and
	// nothing in the metadata would have said so. Invariant 15 makes a new
	// capability a new contract entry for exactly this reason.
	//
	// There is no `SatisfiedBy`. No other contract in the registry can stand
	// in for an agent: promoting `inference` or `modelSource` into this slot
	// would hand a consumer a raw model where it asked for an agent, and it
	// could not tell the difference until it sent a tool call and got a
	// completion back.
	//
	// `apiKey` is the bearer the provider's own agent listener expects. It is
	// minted and published by the provider, so revoking it revokes agent
	// access and nothing else: it is not the provider's admin credential and
	// never a credential of anything behind it.
	//
	// The contract carries no `path` value. The endpoint prefix is a property
	// of the port the provider serves this contract on, so it is declared
	// once on that `extraPorts` entry and read from there; declaring it here
	// as well would give the Traefik route and the consumer's URL two
	// sources to drift apart.
	{
		Name:    "agentApi",
		Secrets: []string{"apiKey"},
		Values: []ValueSpec{
			// The model id a consumer should request. Optional because an
			// agent may have no preference and a consumer's own default
			// then stands.
			{Key: "modelName", Optional: true},
		},
	},

	// A CalDAV/CardDAV server: the endpoint a calendar or contacts client
	// speaks DAV to. Provided by the DAV server itself (Radicale), consumed
	// by anything that wants to show the user the calendars Bloud already
	// serves instead of making them type a server address from memory.
	//
	// The contract carries no credential, and that is the design rather than
	// an omission. A DAV server authenticates the *person*: the password they
	// typed into Thunderbird or DAVx⁵, which the server verifies against the
	// identity provider itself. There is no machine credential for Bloud to
	// hand over, and a contract that could carry one would be a contract for
	// reading a user's password and handing it to a second app. A consumer
	// gets the address and nothing else, which is also exactly what it needs:
	// the client authenticates the user directly, end to end.
	//
	// `path` is the DAV root on the provider's address. It is a value rather
	// than a hardcoded `/` so a provider mounted under a prefix (Nextcloud's
	// `/remote.php/dav`) declares its own truth instead of every consumer
	// assuming the shape of the one server in this catalog.
	{
		Name:   "caldav",
		Values: []ValueSpec{{Key: "path", AbsolutePath: true}},
	},

	// A streamable-HTTP MCP server an agent harness can register as a tool
	// namespace. The consumer is the harness (Hermes and friends); the provider
	// is any app that serves MCP.
	//
	// There is no `SatisfiedBy`. A harness has no meaningful fallback: standing
	// in some other contract for a tool server would hand it credentials for an
	// endpoint that speaks a different protocol, and it could not tell.
	//
	// `httpToken` is the bearer the provider's own MCP listener expects. It is
	// not the provider's admin credential and never the credential of an app
	// behind it: the provider mints a scoped one through its own mechanism, so
	// revoking it revokes MCP access and nothing else.
	//
	// `path` is the endpoint path and `serverName` the tool namespace. Note the
	// binding deliberately does not carry a composed URL. The provider may be
	// reachable from the consumer's network position by one address and not
	// another (a host-networked harness cannot resolve a container name), so
	// the binding hands over BaseURL, LocalURL and the path, and the consumer
	// composes the one its own topology can dial.
	{
		Name:    "mcp",
		Secrets: []string{"httpToken"},
		Values: []ValueSpec{
			{Key: "path", AbsolutePath: true},
			{Key: "serverName"},
		},
	},

	// A credential into another app's own API, for a companion that is not a
	// browser and cannot join the identity provider: an MCP wrapper, a bot, a
	// bridge process. The provider publishes the account's username as a value
	// (it is visible in the provider's own UI, so it is not a secret) and its
	// password as the secret a consumer authenticates with.
	//
	// This contract exists because "the provider is the app" (the shape the
	// `mcp` contract prefers) does not fit a target whose own MCP server is
	// missing, weaker, or read-only. Such a target needs a wrapper, and the
	// wrapper needs a real credential into the target's API: that is precisely
	// what no other contract carries. It is deliberately narrow: it hands over
	// one account's password and nothing else, the account belongs to the
	// provider and only the provider's own sign-in can validate it, and a
	// consumer that does not require the secret gets the username with an empty
	// password, the same rule every other contract follows (invariant 15).
	//
	// The password is the account's real password, not a Bloud invention. A
	// string Bloud generated and published would authenticate against nothing.
	// For a target that removed programmatic tokens (AFFiNE 0.27 removed its
	// personal-access-token API), the account password is the only credential
	// the target still validates.
	{
		Name:    "appApi",
		Secrets: []string{"password"},
		Values: []ValueSpec{
			{Key: "username"},
			// The scope the provider wants a companion to address by default,
			// where the provider's API is organized into scopes. AFFiNE's is the
			// shared workspace Bloud provisions. A provider with no scope omits
			// it; the companion leaves its own default unset.
			{Key: "workspaceId", Optional: true},
		},
	},

	// A credential meant to be copied by a human into a third-party client
	// that cannot join the identity provider: a native phone app, a desktop
	// sync tool, a device with no browser.
	//
	// This is the other half of the audience split. Every other contract in
	// this registry answers "what does a consumer app get?". This one answers
	// "what does a person holding a phone get?", and `clientAccess` on the
	// offer is what makes that second audience explicit rather than implicit.
	// The distinction matters because the two need different controls: a
	// machine credential is rotated by an operator and never displayed, and
	// a human credential has to be shown at least once, copied by hand, and
	// survived losing.
	//
	// It is a separate contract from `appApi` rather than a flag on it. The
	// `appApi` payload is a real account's real password, deliberately handed
	// to a companion process that Bloud itself configured. Folding a
	// human-copyable credential into it would put that same value on a
	// surface designed to be copied onto a phone, and the two have different
	// blast radii: a companion's credential is one process Bloud placed, a
	// human-copied one is wherever the person put it. Invariant 15 makes a
	// new capability a new contract entry for exactly this reason.
	//
	// The contract itself carries no policy. Reveal, rotate, and the
	// disclosure all live on the provider's `clientAccess` block, because
	// they are facts about the app being revealed rather than about the shape
	// of the credential, and the loader holds that block to a stricter bar
	// than it holds anything else in an offer.
	{
		Name:    "clientPassword",
		Secrets: []string{"password"},
	},
}

// ContractFor returns the contract with the given name.
func ContractFor(name string) (Contract, bool) {
	for _, contract := range contracts {
		if contract.Name == name {
			return contract, true
		}
	}
	return Contract{}, false
}

// ContractNames returns every known contract name, for error messages.
func ContractNames() []string {
	names := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		names = append(names, contract.Name)
	}
	return names
}
