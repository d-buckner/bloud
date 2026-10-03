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

	// The control-plane listener of a running agent, for a front end that
	// drives it. The provider is the agent; the consumer is its UI.
	//
	// The provider owns the gateway end to end. It mints the credential,
	// hands it to its own listener, and publishes it here. A consumer never
	// configures a gateway it is not running, and never reaches into the
	// provider's `.env` or config file to find the key out: the only thing a
	// front end gets is what the agent chose to publish. That is why this is
	// a contract rather than a shared file, and why the secret is the
	// provider's to mint even though the consumer is the one that will
	// eventually present it.
	//
	// `path` is the API root on the provider's address. Declared rather than
	// assumed so a provider that mounts its surface somewhere else states its
	// own truth instead of every consumer guessing the shape of the one agent
	// in this catalog.
	{
		Name:    "agentGateway",
		Secrets: []string{"httpToken"},
		Values:  []ValueSpec{{Key: "path", AbsolutePath: true}},
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
