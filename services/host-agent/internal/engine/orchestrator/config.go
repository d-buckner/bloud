// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

// config.go declares the outbound dependency interfaces the orchestrator
// package requires from the outside world. All of these are fields of
// OrchestratorConfig (defined in orchestrator.go) and are set once at
// construction time via NewOrchestrator.

import (
	"context"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// ── Dependency interfaces ─────────────────────────────────────────────────

// SSOProvisioner provisions per-app SSO in the identity provider (e.g. Authentik).
// Implementations must be idempotent: called on every ensureApp, not just first install.
type SSOProvisioner interface {
	// EnsureForwardAuth creates or verifies the proxy provider + application for a
	// forward-auth app, and adds it to the embedded outpost.
	EnsureForwardAuth(ctx context.Context, appName, displayName, externalURL string) error

	// EnsureNativeOIDC creates or verifies the OAuth2 provider + application for a
	// native-oidc app. redirectURIs must cover every URL the app may use as its
	// callback (all base URLs plus the direct-port debug URL). tuning carries the
	// app's optional extra scopes and access token lifetime (zero = defaults).
	EnsureNativeOIDC(ctx context.Context, appName, displayName, clientID, clientSecret string, redirectURIs []string, launchURL string, tuning authentik.OIDCTuning) error

	// Deprovision removes the SSO provider and application an app's previous
	// strategy created, so a strategy change converges away from the old
	// wiring. Implementations must be idempotent: deleting a provider that is
	// already gone is a no-op. Strategies with no per-app provider ("none",
	// "ldap") deprovision nothing.
	Deprovision(ctx context.Context, appName, displayName, ssoStrategy string) error
}
