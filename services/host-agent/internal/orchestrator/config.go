package orchestrator

// config.go declares the outbound dependency interfaces the orchestrator
// package requires from the outside world. All of these are fields of
// OrchestratorConfig (defined in orchestrator.go) and are set once at
// construction time via NewOrchestrator.

import (
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
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
