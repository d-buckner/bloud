// SPDX-License-Identifier: AGPL-3.0-only

// Package configurator provides the interface and utilities for app configuration.
// Configurators handle app-specific setup that can't be expressed in static
// config (manifests / container defs): config-file generation, API-based setup.
package configurator

import (
	"context"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
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
	// DeleteAppSecrets removes every credential published under one scope. It
	// is the teardown counterpart of SetAppSecret: when a provider record goes
	// away, the key that belonged to it goes with it rather than sitting in the
	// secrets file with nothing left to own it.
	DeleteAppSecrets(appName string) error
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

// PhaseBudget is how long the framework lets one NodeLifecycle phase run for
// one app before cancelling it. It applies to PreStart and to PostStart, each
// with its own full allowance.
//
// The unit is the app, not the pass. The orchestrator is the single writer, so
// a configurator that hangs does not hang one app: it hangs every install,
// uninstall, and address change queued behind it, with nothing to cut it off.
// These two phases are the only ones that run app-supplied code, and they were
// the only ones with no ceiling. Container ops and health checks are out of
// scope because they already carry their own bounds (HealthCheckTimeout and the
// per-container healthCheck.timeout).
//
// Each phase gets the full budget independently rather than drawing from one
// shared per-app pool, so a slow PreStart cannot leave PostStart short of the
// room a declared readiness wait needs. The trade is that one app's worst case
// is two budgets rather than one, which buys never having to attribute a
// deadline to the phase that did not consume the time.
//
// An app's own declared readiness wait (appclient.Within) has to fit inside
// this ceiling or the framework cancels it before its own deadline can fire.
// The apps/configtest harness enforces that against this constant, so a wait
// that could never complete fails the build instead of the install.
const PhaseBudget = 5 * time.Minute

// SessionRevoker is implemented by configurators whose app keeps a session
// store that Bloud can clear. It is optional and separate from NodeLifecycle
// for the same reason Remover is: most apps have nothing to revoke through the
// host, and an interface every configurator had to implement would be noise.
//
// Revocation is the only operation that ends a live session. Rotating a
// credential does not, because a session is signed under the app's own key and
// records no binding to the credential that minted it, so there is nothing to
// join a revoked credential against. Clearing the store is what makes those
// sessions stop verifying.
//
// Implementations must be idempotent: the orchestrator calls this from the
// resync path, and a second call against an already-empty store must succeed.
type SessionRevoker interface {
	// RevokeSessions clears the app's persisted sessions. It runs before the
	// container is recreated, so the app comes back with an empty store rather
	// than one it re-read from disk at boot.
	RevokeSessions(ctx context.Context, state *AppState) error
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

// PortReporter is implemented by a configurator that carries the app's port.
// It is optional and separate from NodeLifecycle: a node that writes config for
// a container it does not own has no port to report, and says so by not
// implementing this interface.
//
// The value is the port the constructor resolved, which is the catalog's
// `port:` when registration passed 0. The conformance harness compares it
// against `metadata.yaml`, because the same number is written in both places
// and only one of them routes traffic. A configurator that reports a different
// port builds URLs that connect somewhere else.
type PortReporter interface {
	// Port returns the port this node's app is reached on.
	Port() int
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
	Integrations bindings.Integrations
}
