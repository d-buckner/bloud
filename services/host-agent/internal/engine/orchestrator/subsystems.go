// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"time"

	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// TuningConfig groups the tunable parameters and the event bus. Zero values
// mean "use the default" or "disabled", per field.
type TuningConfig struct {
	// HealthCheckTimeout limits how long each app's HealthCheck can run.
	// Zero means no timeout (the caller's context deadline applies).
	HealthCheckTimeout time.Duration
	// AppPhaseBudget bounds one configurator phase -- PreStart or PostStart --
	// for one app before the framework cancels it. Each phase gets its own full
	// allowance. Zero means DefaultAppPhaseBudget.
	AppPhaseBudget time.Duration
	// ResyncRestartWarnAt is how many consecutive resync-triggered restarts
	// one node accumulates before the watchdog raises a signal. Zero means
	// DefaultResyncRestartWarnAt. It changes nothing about whether a restart
	// happens: the watchdog observes and reports, it never withholds.
	ResyncRestartWarnAt int
	// ResyncCostBudget is the wall clock one node's config resync is expected to
	// fit inside. Zero means DefaultResyncCostBudget. Exceeding it changes nothing
	// about whether the resync runs; it is what makes the node loud.
	ResyncCostBudget time.Duration
	// ResyncCostWarnAt is how many consecutive over-budget resyncs one node
	// accumulates before the watch raises a signal. Zero means
	// DefaultResyncCostWarnAt.
	ResyncCostWarnAt int
	// SelfHealInterval is how long the instance may sit without a convergence
	// pass before the self-healing timer submits one. Zero means no periodic
	// pass (a hand-built orchestrator stays quiet); wire.Build always supplies
	// DefaultSelfHealInterval unless BLOUD_RECONCILE_INTERVAL was set.
	SelfHealInterval time.Duration
	// Events is the bus used to broadcast lifecycle transitions and activity
	// to API subscribers (SSE). Nil disables event publishing.
	Events *eventbus.Bus
}

// RuntimeConfig groups the container runtime and the config-generation pieces
// that depend on it. Nil fields disable catalog-driven container creation.
type RuntimeConfig struct {
	// Containers is the container runtime used to create app containers from
	// catalog specs.
	Containers containerruntime.Runtime
	// TemplateVars are the extra variables container-spec templates render
	// with (postgresPassword and the authentik values). A store, not a bare
	// map, because the authentik configurator writes one value at runtime.
	TemplateVars *configurator.TemplateVars
	// TraefikGen renders the Traefik dynamic config from the installed set.
	TraefikGen traefikgen.GeneratorInterface
	// TraefikPort is the port the public entrypoint listens on. LAN IP base
	// URLs are built on it. See HostSet.AllBaseURLs.
	TraefikPort int
}

// StoresConfig groups the persisted-state collaborators the orchestrator reads
// intent from and writes lifecycle state to. Nil disables the subsystem that
// reads the field.
type StoresConfig struct {
	AppStore store.AppStoreInterface
	// Settings persists the instance-level settings, including the public
	// address (nil = not supported).
	Settings store.SettingsStoreInterface
	// Operations persists durable lifecycle operation state. Nil disables the
	// recorder.
	Operations *store.OperationStore
	// Secrets is the host secret store. Nil disables publishing: integration
	// bindings still carry the provider's identity and address.
	Secrets configurator.AppSecretsProvider
	// ExternalApps is the operator-declared external app registry. Nil
	// disables the feature (external apps are simply absent).
	ExternalApps store.ExternalAppStoreInterface
}

// SSOConfig groups the identity-provider provisioning and the OIDC/LDAP
// outputs handed to app configurators. The SSOBaseURL/SSOAuthentikURL/
// SSOIssuerURL strings are the legacy single-host settings; Hosts supersedes
// them when non-nil.
type SSOConfig struct {
	SSO SSOProvisioner
	// LDAPOutput is the LDAP provider endpoint injected into apps with LDAP
	// SSO strategy. Nil when no LDAP provider is configured.
	LDAPOutput      *configurator.LDAPOutput
	SSOBaseURL      string // base URL for building app subdomain URLs
	SSOHostSecret   string // master secret for deriving deterministic per-app OIDC client secrets
	SSOAuthentikURL string // browser-accessible Authentik URL for OIDC issuer/discovery
	SSOIssuerURL    string // OIDC issuer base URL reachable from app containers (empty = SSOAuthentikURL)
}

// HostsConfig groups the live address state. When Hosts is non-nil it
// supersedes the legacy SSO URL strings.
type HostsConfig struct {
	Hosts          *hostset.State
	OnHostsChanged func() // fires after a SetPublicURL intent is applied
}
