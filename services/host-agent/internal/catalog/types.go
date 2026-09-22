// SPDX-License-Identifier: AGPL-3.0-only

package catalog

// AppDefinition represents an application in the catalog
type AppDefinition struct {
	Name         string                 `yaml:"name" json:"name"`
	Integrations map[string]Integration `yaml:"integrations" json:"integrations"`
}

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
// another contract's payload. An app that offers several (an MCP server that is
// also a media server) declares one entry per contract.
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
}

// CompatibleApp defines a specific app that can fulfill an integration
type CompatibleApp struct {
	App      string `yaml:"app" json:"app"`
	Default  bool   `yaml:"default,omitempty" json:"default,omitempty"`
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
}

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
