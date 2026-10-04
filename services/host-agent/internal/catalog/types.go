// SPDX-License-Identifier: AGPL-3.0-only

package catalog

// Integration defines how an app connects to other apps.
type Integration struct {
	Required bool `yaml:"required" json:"required"`
	Multi    bool `yaml:"multi" json:"multi"`
	// Requires lists the secret names this consumer actually reads out of the
	// contract's payload. Only those are resolved, so an app is handed the
	// credentials it declared a need for rather than everything its provider
	// offers: declaring `sso` does not, by itself, hand an app the identity
	// provider's API token. A name that is not part of the contract's
	// requirements fails the catalog load.
	Requires   []string        `yaml:"requires,omitempty" json:"requires,omitempty"`
	Compatible []CompatibleApp `yaml:"compatible" json:"compatible"`
}

// Provides declares, per integration contract, what an app offers to the
// consumers that integrate with it. Integration declares the consumer side
// (which providers are compatible, under a contract name); Provides is the
// provider side, keyed by the same contract names, and it is what makes a value
// visible to a consumer at all.
//
// Keying by contract is what keeps the offer honest: a PVR's API key is offered
// to whoever integrates with it *as a PVR*, not to every consumer that happens
// to have a binding, and a consumer declaring one contract can never read
// another contract's payload. An app that offers several contracts declares one
// entry per contract.
//
// The names and the required values are defined in contracts.go: a declaration
// that does not match its contract fails the catalog load rather than reaching a
// consumer as a half-empty binding.
type Provides map[string]ContractProvides

// ContractProvides is what a provider offers under one contract.
type ContractProvides struct {
	// Secrets lists the credentials this app publishes for this contract. A
	// value is stored under the app's own name in the host secret store (the app
	// writes it with SetAppSecret, or the host already generated it) and reaches
	// a consumer as the corresponding field of its binding. Names an app does not
	// list here are never handed to another app, however they were stored.
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	// Values are the non-secret facts this contract carries, e.g. an endpoint
	// path. They travel in the provider's metadata, so they need no publication
	// step.
	Values map[string]string `yaml:"values,omitempty" json:"values,omitempty"`
	// RuntimeValues names the value keys this provider fills at runtime instead
	// of declaring in Values, because the value does not exist until the app is
	// up. AFFiNE's MCP endpoint is `/api/workspaces/<id>/mcp` and the id is
	// minted by AFFiNE on first boot, so no metadata file could state it.
	//
	// Declaring it here rather than leaving the loader to accept a missing value
	// is what keeps the channel honest: the loader rejects a key that is neither
	// declared statically nor listed as runtime-supplied, and rejects one listed
	// both ways, so a value always has exactly one authoritative source. A
	// configurator fills it with SetAppContractValue.
	RuntimeValues []string `yaml:"runtimeValues,omitempty" json:"runtimeValues,omitempty"`
}

// CompatibleApp defines a specific provider that can fulfill an integration.
//
// Exactly one of App and Source names the provider. App is the normal case: an
// installed catalog app. Source: "instance" means the provider is the instance
// itself, a value the operator configured in Settings rather than a thing Bloud
// runs. An instance provider creates no graph node and has no container, which is
// why it is a source and not a catalog entry.
type CompatibleApp struct {
	App      string `yaml:"app,omitempty" json:"app,omitempty"`
	Source   string `yaml:"source,omitempty" json:"source,omitempty"`
	Default  bool   `yaml:"default,omitempty" json:"default,omitempty"`
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
}

// InstanceProviderSource is the CompatibleApp.Source value naming the instance
// settings as a contract provider, and the ProviderRef.App value it carries.
// It is reserved: no catalog app may be named "instance".
const InstanceProviderSource = "instance"

// IntegrationRef is a back-pointer: which app needs this app, for what integration
type IntegrationRef struct {
	App         string `json:"app"`
	Integration string `json:"integration"`
}

// ConfigTask represents a configuration action to perform
type ConfigTask struct {
	Target      string `json:"target"`
	Source      string `json:"source"`
	Integration string `json:"integration"`
}
