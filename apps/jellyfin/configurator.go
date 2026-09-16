// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// appName is the secrets-manager key for Jellyfin's generated credentials.
	appName = "jellyfin"
	// bootstrapUsername is the managed admin account used for setup and
	// subsequent reconciliation. Its password is generated per-deployment and
	// persisted by the secrets manager — never hardcoded here.
	bootstrapUsername = "bloud-bootstrap-admin"

	// LDAP plugin GUID - this is the standard ID for the Jellyfin LDAP-Auth plugin
	// Note: Jellyfin uses GUIDs without dashes in the API
	ldapPluginID     = "958aad6637844d2ab89aa7b6fab6e25c"
	ldapPluginURL    = "https://repo.jellyfin.org/files/plugin/ldap-authentication/ldap-authentication_23.0.0.0.zip"
	ldapPluginSHA256 = "952e33fa8d3ac512ccb5c1e2e1c655cbb1957e41fa1f00bd6ccf3076e0467446"
)

// Configurator handles Jellyfin configuration

// Configurator handles Jellyfin configuration
type Configurator struct {
	Port         int
	baseURL      string // Override for testing; if empty, uses localhost:Port
	pluginURL    string
	pluginSHA256 string
	secrets      configurator.AppSecretsProvider
	logger       *slog.Logger
}

// NewConfigurator creates a new Jellyfin configurator

// NewConfigurator creates a new Jellyfin configurator
func NewConfigurator(port int, secrets configurator.AppSecretsProvider, logger *slog.Logger) *Configurator {
	if port == 0 {
		port = 8096
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Configurator{
		Port:         port,
		secrets:      secrets,
		pluginURL:    ldapPluginURL,
		pluginSHA256: ldapPluginSHA256,
		logger:       logger.With("app", "jellyfin"),
	}
}

// resolveAdminPassword returns the durable, per-deployment bootstrap admin
// password from the secrets manager (generated on first call, then stable across
// reconciliations). The password is never a hardcoded constant.

// resolveAdminPassword returns the durable, per-deployment bootstrap admin
// password from the secrets manager (generated on first call, then stable across
// reconciliations). The password is never a hardcoded constant.
func (c *Configurator) resolveAdminPassword() (string, error) {
	if c.secrets == nil {
		return "", fmt.Errorf("no secrets provider for Jellyfin bootstrap admin")
	}
	pw, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("generating Jellyfin admin password: %w", err)
	}
	return pw, nil
}

// getBaseURL returns the base URL for API calls

// getBaseURL returns the base URL for API calls
func (c *Configurator) getBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

func (c *Configurator) Name() string {
	return "apps-jellyfin"
}

// PreStart ensures directories exist, installs the LDAP plugin, and
// configures network settings.

// PreStart ensures directories exist, installs the LDAP plugin, and
// configures network settings.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (bool, error) {
	c.logger.Info("PreStart: creating data directories")
	dirs := []string{
		filepath.Join(state.DataPath, "config"),
		filepath.Join(state.DataPath, "cache"),
		filepath.Join(state.BloudDataPath, "media", "movies"),
		filepath.Join(state.BloudDataPath, "media", "shows"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	c.logger.Info("PreStart: ensuring LDAP plugin")
	pluginInstalled, err := c.ensureLDAPPlugin(ctx, state.DataPath)
	if err != nil {
		return false, fmt.Errorf("failed to install LDAP plugin: %w", err)
	}

	c.logger.Info("PreStart: configuring network")
	networkChanged, err := c.configureNetwork(state.DataPath)
	if err != nil {
		return false, fmt.Errorf("failed to configure network: %w", err)
	}

	c.logger.Info("PreStart complete", "plugin_installed", pluginInstalled, "network_changed", networkChanged)
	return pluginInstalled || networkChanged, nil
}

// Remove is a no-op for the Jellyfin configurator; container and data removal
// are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart completes the Jellyfin setup wizard and configures LDAP.
// It runs on a context detached from the convergence pass with its own
// 90 s deadline so the 503-retry loop and network steps survive the pass
// completing. See the inline comment for the context-detach rationale.

// PostStart completes the Jellyfin setup wizard and configures LDAP.
// It runs on a context detached from the convergence pass with its own
// 90 s deadline so the 503-retry loop and network steps survive the pass
// completing. See the inline comment for the context-detach rationale.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// The pass context is short-lived and is cancelled when the pass
	// completes, but PostStart can outlive the pass — the 503-retry loop
	// sleeps between attempts, and the wizard/library/LDAP steps make
	// network calls. If any of those are bound to the pass context, the
	// cancellation surfaces as "context canceled" mid-step, the node goes
	// to terminal ERROR, and the reconciler never retries it.
	//
	// context.Background() detaches completely from the pass so the retry
	// loop and subsequent steps survive the pass completing. A 90 s
	// deadline bounds the work; the outer e2e timeout is the real backstop.
	// (context.WithoutCancel was tried first but did not prevent the
	// cancellation on Go 1.25 linux/amd64 — the retry loop still broke
	// at the ctx.Err() check after the first getSystemInfo call.)
	runCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return c.postStart(runCtx, state)
}

// postStart contains the PostStart body. It receives a detached context with
// its own deadline, so the 503-retry loop and the wizard/library/LDAP steps
// survive the convergence pass completing.

// postStart contains the PostStart body. It receives a detached context with
// its own deadline, so the 503-retry loop and the wizard/library/LDAP steps
// survive the convergence pass completing.
func (c *Configurator) postStart(ctx context.Context, state *configurator.AppState) error {
	c.logger.Info("DBG postStart: entered", "ctx_err", ctx.Err(), "base_url", c.getBaseURL())
	c.logger.Info("PostStart: checking setup wizard status")

	// 1. Check if setup wizard is complete.
	info, err := c.waitForSystemInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get system info: %w", err)
	}
	info = c.awaitWizardCompletion(ctx, info)

	if !info.StartupWizardCompleted {
		c.logger.Info("completing setup wizard")
		if err := c.completeStartupWizard(ctx); err != nil {
			return fmt.Errorf("failed to complete startup wizard: %w", err)
		}
		c.logger.Info("setup wizard completed")
	} else {
		c.logger.Info("setup wizard already complete")
	}

	// 2. Configure media libraries
	c.logger.Info("PostStart: configuring media libraries")
	if err := c.configureLibraries(ctx); err != nil {
		return fmt.Errorf("failed to configure libraries: %w", err)
	}

	// 3. Configure LDAP if SSO integration is enabled and LDAP output is available
	if state.SSOEnabled && state.LDAP != nil {
		c.logger.Info("PostStart: configuring LDAP", "ldap_host", state.LDAP.Host, "ldap_port", state.LDAP.Port)
		if err := c.configureLDAP(ctx, state); err != nil {
			return fmt.Errorf("failed to configure LDAP: %w", err)
		}
	} else {
		c.logger.Info("PostStart: skipping LDAP config", "sso_enabled", state.SSOEnabled, "ldap_configured", state.LDAP != nil)
	}

	c.logger.Info("PostStart complete")
	return nil
}

// waitForSystemInfo polls /System/Info until it answers or the context ends.
// The container health check (curl -sf /System/Info/Public) passes on the
// first 200, but Jellyfin oscillates during first-run init — it briefly
// returns 200 then drops back to 503 "Server is loading" before
// stabilising. A single 503 here would fail PostStart, which the reconciler
// treats as a terminal node ERROR it never retries, so wait out the
// transient instead. ctx is detached from the pass (see PostStart), so the
// 2 s sleep between attempts cannot be cancelled mid-pass; the loop is
// bounded by the deadline on ctx.
