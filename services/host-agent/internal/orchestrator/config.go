package orchestrator

// config.go is the wiring contract for the orchestrator convergence pipeline.
// It declares every outbound port (dependency interface) the package needs from
// the outside world, the ConvergeConfig struct that groups them, and the
// WithConvergeConfig setter that wires them into the Orchestrator.
//
// Reading this file answers the question: "what must a caller supply to run
// a full convergence pass?"

import (
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
)

// ── Dependency interfaces ─────────────────────────────────────────────────

// TailnetNodeEnsurer abstracts tailnet node container lifecycle.
type TailnetNodeEnsurer interface {
	EnsureRunning(ctx context.Context, appName string) error
	StopAndPurge(ctx context.Context, appName string) error
}

// GatewayManager abstracts the gateway container: it ensures the gateway is
// running, can purge it, and discovers the tailnet MagicDNS domain from it.
type GatewayManager interface {
	EnsureRunning(ctx context.Context) error
	StopAndPurge(ctx context.Context) error
	GetTailnetDomain(ctx context.Context) (string, error)
}

// RemoteProxyManager manages per-remote-app reverse proxies and supports
// stopping all proxies at once.
type RemoteProxyManager interface {
	StopAll()
	Reconcile(targets []sharing.ProxyTarget) map[string]int
}

// ForwardDomainProvisioner provisions a forward_domain SSO provider for a
// tailnet domain. Returns the outpost API token needed to start the standalone
// proxy outpost container.
type ForwardDomainProvisioner interface {
	EnsureForwardDomainAuth(cookieDomain string) (token string, err error)
}

// ProxyOutpostEnsurer abstracts the standalone proxy outpost container lifecycle.
type ProxyOutpostEnsurer interface {
	EnsureRunning(ctx context.Context, token, tailnetDomain string) error
	Stop(ctx context.Context) error
}

// SSOProvisioner provisions per-app SSO in the identity provider (e.g. Authentik).
// Implementations must be idempotent — called on every ensureApp, not just first install.
type SSOProvisioner interface {
	// EnsureForwardAuth creates or verifies the proxy provider + application for a
	// forward-auth app, and adds it to the embedded outpost.
	EnsureForwardAuth(appName, displayName, externalURL string) error
}

// ── Wiring contract ───────────────────────────────────────────────────────

// ConvergeConfig holds the complete set of dependencies required for a
// convergence pass. Nil/zero fields disable the corresponding subsystem.
type ConvergeConfig struct {
	AppStore         store.AppStoreInterface
	CatalogGraph     catalog.AppGraphInterface
	TailnetStore     store.TailnetStoreInterface
	RemoteAppStore   store.RemoteAppStoreInterface
	TailnetNode      TailnetNodeEnsurer
	Gateway          GatewayManager
	RemoteProxy      RemoteProxyManager
	ProxyOutpost     ProxyOutpostEnsurer
	ForwardDomainSSO ForwardDomainProvisioner
	SSO              SSOProvisioner
	SSOBaseURL       string // base URL for building app subdomain URLs (e.g. "http://localhost:8080")
	TraefikGen       traefikgen.GeneratorInterface
	ActiveTailnetID  func() string // returns the active tailnet connection ID (empty if none)
}

// WithConvergeConfig wires the convergence dependencies into the orchestrator.
// Returns the receiver for chaining.
func (o *Orchestrator) WithConvergeConfig(cfg ConvergeConfig) *Orchestrator {
	o.appStore = cfg.AppStore
	o.catalogGraph = cfg.CatalogGraph
	o.tailnetStore = cfg.TailnetStore
	o.remoteAppStore = cfg.RemoteAppStore
	o.tailnetNode = cfg.TailnetNode
	o.gateway = cfg.Gateway
	o.remoteProxy = cfg.RemoteProxy
	o.proxyOutpost = cfg.ProxyOutpost
	o.forwardDomainSSO = cfg.ForwardDomainSSO
	o.sso = cfg.SSO
	o.ssoBaseURL = cfg.SSOBaseURL
	o.traefikGen = cfg.TraefikGen
	o.activeTailnetID = cfg.ActiveTailnetID
	return o
}
