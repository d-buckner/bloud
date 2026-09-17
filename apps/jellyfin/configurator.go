// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package jellyfin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
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

// Configurator handles Jellyfin configuration. It owns the orchestration of
// the typed API client (api.go) and the static asset installer; all raw HTTP
// lives in appclient behind the client.
type Configurator struct {
	Port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *jellyfinAPI
	assets  appasset.Installer

	// baseURL is a test seam: when set, the API client resolves to it instead
	// of localhost:Port. (The asset installer takes its URL from pluginURL.)
	baseURL string
	// pluginURL / pluginSHA256 are the LDAP plugin source + digest; overridable
	// in tests so the download path is exercised without the real CDN.
	pluginURL    string
	pluginSHA256 string
}

// NewConfigurator creates a new Jellyfin configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = 8096
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		Port:         port,
		secrets:      deps.Secrets,
		logger:       logger.With("app", "jellyfin"),
		assets:       deps.Assets,
		pluginURL:    ldapPluginURL,
		pluginSHA256: ldapPluginSHA256,
	}
	c.api = newAPI(deps.HTTP, func() string {
		if c.baseURL != "" {
			return c.baseURL
		}
		return fmt.Sprintf("http://localhost:%d", c.Port)
	})
	return c
}

// Name returns the node name this configurator manages.
func (c *Configurator) Name() string {
	return "apps-jellyfin"
}

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

// PreStart ensures directories exist, installs the LDAP plugin, and
// configures network settings.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (bool, error) {
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

	pluginInstalled, err := c.ensureLDAPPlugin(ctx, state.DataPath)
	if err != nil {
		return false, fmt.Errorf("failed to install LDAP plugin: %w", err)
	}

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
//
// It runs under the framework's PostStartBudget: the orchestrator bounds the
// finalization wait and cancels it on shutdown, so the app uses the pass ctx
// directly rather than detaching with its own Background deadline. The wizard
// readiness waits survive a pass because the pass ctx is process-scoped (only
// Stop cancels it), not because the app detached.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	return c.postStart(ctx, state)
}

// postStart contains the PostStart body.
func (c *Configurator) postStart(ctx context.Context, state *configurator.AppState) error {
	c.logger.Info("PostStart: checking setup wizard status")

	// 1. Check if setup wizard is complete.
	info, err := c.api.waitForSystemInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get system info: %w", err)
	}
	info = c.api.awaitWizardCompletion(ctx, info)

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
