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
	// Port names which of the provider's ports this contract is served on.
	// It is an ExtraPort name, not a number: the resolver looks the name up
	// in the provider's `extraPorts` and composes the address from what it
	// finds there. Empty means the app's main `port`, which is the case for
	// every contract in the catalog until an app serves more than one thing.
	//
	// Naming a port rather than repeating one is what keeps a multi-port
	// provider honest: the number lives in exactly one place, so a consumer
	// cannot be handed an address for a surface the provider moved, renamed,
	// or never exposed. A name that resolves to nothing fails the catalog
	// load rather than reaching a consumer as an address that connects to
	// nothing.
	Port string `yaml:"port,omitempty" json:"port,omitempty"`
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
	// ClientAccess declares that this contract's credential is meant to reach
	// a third-party client a human holds, not only a consumer app on the
	// graph. It is the field that makes that second audience explicit rather
	// than implicit, and it is what the reveal / rotate / revoke surface is
	// generated from.
	//
	// Absent means no client access: the credential stays machine-only, which
	// is the case for every contract written before this field existed. A
	// provider that sets it is asserting that a human may legitimately hold
	// this value, so the loader holds it to a stricter account than it holds
	// the rest of the offer: an absent block is a silent "no", a present one
	// has to justify itself in prose the user will read.
	ClientAccess *ClientAccess `yaml:"clientAccess,omitempty" json:"clientAccess,omitempty"`
}

// ClientReveal is how long a client credential may be read back by an
// operator.
type ClientReveal string

const (
	// ClientRevealNever is the default and the safe answer: the credential is
	// never displayed. The value exists and is wired, and no surface reads it
	// back. A provider that sets `reveal: never` explicitly is saying the
	// same thing as omitting the block, which is useful when the block is
	// present for `rotate` and the credential still should not be shown.
	ClientRevealNever ClientReveal = "never"

	// ClientRevealOnce displays the credential exactly once, in the flow
	// that created it, and never again. This is the shape a user is meant to
	// live with: copy it into the client now, and if it is lost, rotate
	// rather than re-read.
	//
	// It is only survivable because a credential and the sessions minted from
	// it are independent: rotating the value does not log out a client that
	// already signed in, so "lost the password" is not "lost the device".
	ClientRevealOnce ClientReveal = "once"

	// ClientRevealAlways lets the credential be read back at any time. It
	// is the most convenient value and the one that must not be granted
	// lightly: it means a long-lived plaintext read on an API whose admin
	// position is currently forgeable from any client that can add a header
	// (see the auth-bypass entry in docs/operations/tech-debt.md). Until
	// that is repaid, a provider declaring `always` is shipping a remote
	// plaintext endpoint.
	ClientRevealAlways ClientReveal = "always"
)

// ClientRotate names who can produce a new value for this credential.
//
// It is a separate axis from revocation. Rotating changes what authenticates
// future sign-ins; it does not end sessions that already exist, because a
// session records no binding to the credential that minted it. Making rotate
// imply revoke is a planned strengthening of the pattern, not its current
// shape.
type ClientRotate string

const (
	// ClientRotateBloud means Bloud owns the value: it mints it, stores it,
	// and can regenerate it on demand.
	ClientRotateBloud ClientRotate = "bloud"

	// ClientRotateProvider means the app owns the value. Bloud can ask for a
	// rotation but the app performs it through its own mechanism, so the
	// new value may not round-trip through Bloud's secret store.
	ClientRotateProvider ClientRotate = "provider"

	// ClientRotateNone means the credential is fixed: set once by the
	// provider or the operator and never regenerated. Reveal is still a
	// separate question, which is why the two are separate fields.
	ClientRotateNone ClientRotate = "none"
)

// Client snippet shapes. A snippet is the rendered form the reveal surface
// hands the user, because "here is a secret" is less useful than "here is
// what to type into the client".
const (
	// SnippetURLAndPassword renders the instance URL and the credential as
	// the two fields a mobile client asks for.
	SnippetURLAndPassword = "url-and-password"
	// SnippetURLAndKey renders the instance URL and a bearer-style key.
	SnippetURLAndKey = "url-and-key"
	// SnippetConfigBlock renders a copy-paste config fragment.
	SnippetConfigBlock = "config-block"
)

// ClientAccess is the provider's declaration that a contract's credential is
// meant to be handed to a client a human holds.
//
// The field is deliberately small and deliberately requires prose. `reaches`
// is not decoration: it is the sentence the user reads before copying a
// credential onto a phone, and a reveal surface that shows a secret without
// saying what the secret opens is a surface that trains people to click
// through the warning.
type ClientAccess struct {
	// Secret names which of the offer's published secrets this block
	// describes. Required when the offer publishes more than one, so a
	// provider cannot leave it ambiguous which credential a reveal surface is
	// about to print. With exactly one published secret it may be omitted and
	// that secret is meant.
	Secret string `yaml:"secret,omitempty" json:"secret,omitempty"`
	// Reveal is the read-back policy. Empty is treated as `never`.
	Reveal ClientReveal `yaml:"reveal,omitempty" json:"reveal,omitempty"`
	// Rotate names who can produce a new value. Empty is treated as `none`.
	Rotate ClientRotate `yaml:"rotate,omitempty" json:"rotate,omitempty"`
	// Label is the short human name for this credential, shown wherever the
	// surface lists more than one.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// Reaches is the one-sentence disclosure of what holding this credential
	// grants, written for the person copying it rather than for the integrator.
	// Required whenever Reveal is not `never`.
	Reaches string `yaml:"reaches,omitempty" json:"reaches,omitempty"`
	// Snippet names the rendered form of the reveal. Empty means the surface
	// shows the bare value.
	Snippet string `yaml:"snippet,omitempty" json:"snippet,omitempty"`
}

// Revealable reports whether this credential may be shown to an operator at
// all. A nil block is not revealable, which is what keeps every contract that
// predates this field out of the reveal surface without each one having to say
// so.
func (c *ClientAccess) Revealable() bool {
	return c != nil && c.Reveal != ClientRevealNever && c.Reveal != ""
}

// RotateAllowed reports whether Bloud can regenerate the value.
func (c *ClientAccess) RotateAllowed() bool {
	return c != nil && (c.Rotate == ClientRotateBloud || c.Rotate == ClientRotateProvider)
}

// EffectiveReveal returns the reveal policy with the default applied, so a
// caller never has to re-derive "empty means never".
func (c *ClientAccess) EffectiveReveal() ClientReveal {
	if c == nil || c.Reveal == "" {
		return ClientRevealNever
	}
	return c.Reveal
}

// EffectiveRotate returns the rotate policy with the default applied.
func (c *ClientAccess) EffectiveRotate() ClientRotate {
	if c == nil || c.Rotate == "" {
		return ClientRotateNone
	}
	return c.Rotate
}

// CompatibleApp defines a specific provider that can fulfill an integration.
//
// Exactly one of App and Source names the provider. App is the normal case: an
// installed catalog app. Source: "setting" means the provider is a value the
// operator configured in Settings rather than a thing Bloud runs. A setting
// provider creates no graph node and has no container, which is why it is a
// source and not a catalog entry.
type CompatibleApp struct {
	App      string `yaml:"app,omitempty" json:"app,omitempty"`
	Source   string `yaml:"source,omitempty" json:"source,omitempty"`
	Default  bool   `yaml:"default,omitempty" json:"default,omitempty"`
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
}

// SettingProviderSource is the CompatibleApp.Source value naming an
// operator-declared setting as a contract provider, and the ProviderRef.App
// value it carries. It is reserved: no catalog app may be named "setting".
//
// This is the old `source: instance`. The rename is the point: the value never
// named the instance, it named "a role the operator fills in", which is what a
// `provider` external app with `source: contract:<name>` actually is.
const SettingProviderSource = "setting"

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
