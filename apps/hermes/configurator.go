// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package hermes

import (
	"context"
	"fmt"
	"log/slog"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// appName is the secrets-manager key for Hermes' generated credential.
	appName = "hermes"
	// dashboardUser is the fixed dashboard login name. The password is
	// generated per deployment and persisted by the secrets manager
	// (appSecrets.hermes.adminPassword) — never hardcoded here. The
	// container spec renders the same value into the dashboard's
	// basic-auth env via {{appAdminPassword}}.
	dashboardUser = "bloud"
	// defaultPort is the Hermes dashboard port (upstream default).
	defaultPort = 9119
)

// Configurator handles the Hermes node lifecycle. Hermes owns its config
// (it seeds $HERMES_HOME on first boot inside the data volume), so the
// configurator's job is the credential contract: verifying the durable
// dashboard password resolves before the container is created (PreStart)
// and that the dashboard is serving afterwards (PostStart).
type Configurator struct {
	Port    int
	secrets configurator.AppSecretsProvider
	logger  *slog.Logger
	api     *hermesAPI

	// baseURL is a test seam: when set, the API client resolves to it
	// instead of localhost:Port.
	baseURL string
}

// NewConfigurator creates a new Hermes configurator from the host Deps.
func NewConfigurator(port int, deps configurator.Deps) *Configurator {
	if port == 0 {
		port = defaultPort
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	c := &Configurator{
		Port:    port,
		secrets: deps.Secrets,
		logger:  logger.With("app", "hermes"),
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
	return "apps-hermes"
}

// PreStart verifies the durable dashboard credential resolves before the
// container is built. The container spec renders it into
// HERMES_DASHBOARD_BASIC_AUTH_PASSWORD; failing here surfaces a missing
// secrets provider as a clean install error instead of a container that
// either refuses to boot (the gate fails closed on an empty password) or
// boots with an unintended credential. No files are managed, so
// changed is always false.
func (c *Configurator) PreStart(_ context.Context, _ *configurator.AppState) (bool, error) {
	if _, err := c.resolveAdminPassword(); err != nil {
		return false, err
	}
	c.logger.Info("Hermes dashboard credential ready",
		"username", dashboardUser,
		"secret", "appSecrets.hermes.adminPassword in $BLOUD_DATA_DIR/secrets.json",
	)
	return false, nil
}

// resolveAdminPassword returns the durable, per-deployment dashboard
// password from the secrets manager (generated on first call, stable
// across reconciliations).
func (c *Configurator) resolveAdminPassword() (string, error) {
	if c.secrets == nil {
		return "", fmt.Errorf("no secrets provider for Hermes dashboard credential")
	}
	pw, err := c.secrets.GenerateAppAdminPassword(appName)
	if err != nil {
		return "", fmt.Errorf("generating Hermes dashboard password: %w", err)
	}
	if pw == "" {
		return "", fmt.Errorf("empty Hermes dashboard password from secrets provider")
	}
	return pw, nil
}

// PostStart waits for the dashboard to serve. The auth-gated endpoints
// are irrelevant here: /api/health is exempt from the gate, so the wait
// proves the s6 stack and dashboard process are up without needing the
// credential. Idempotent — on re-reconcile it confirms liveness again.
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.api.waitDashboard(ctx); err != nil {
		return fmt.Errorf("hermes dashboard not reachable: %w", err)
	}
	return nil
}

// Remove is a no-op for the Hermes configurator; container and data
// removal are handled at a higher level by the orchestrator.
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}
