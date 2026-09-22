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

	// A Model Context Protocol server hands the consumer the endpoint to
	// register, plus the bearer token its listener expects.
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
