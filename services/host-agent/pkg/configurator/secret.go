// SPDX-License-Identifier: AGPL-3.0-only

package configurator

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
