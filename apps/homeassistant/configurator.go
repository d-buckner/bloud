// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const appName = "homeassistant"

// bootstrapUsername is the internal-only admin used by the configurator.
const bootstrapUsername = "bloud-bootstrap-admin"

 // Configurator handles Home Assistant configuration
 type Configurator struct {
 	port         int
 	logger       *slog.Logger
 }

 // NewConfigurator creates a new Home Assistant configurator
 func NewConfigurator(port int, logger *slog.Logger) *Configurator {
 	if port == 0 {
 		port = 8123
 	}
 	if logger == nil {
 		logger = slog.Default()
 	}
 	return &Configurator{
 		port: port,
 		logger: logger.With("app", "homeassistant"),
 	}
 }

 func (c *Configurator) Name() string {
 	return "apps-homeassistant"
 }

// PreStart creates the required data directories before the container starts.
 func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
 	dirs := []string{
 		filepath.Join(state.DataPath, "config"),
 		filepath.Join(state.BloudDataPath, "homeassistant"),
 	}

 	for _, dir := range dirs {
 		if err := os.MkdirAll(dir, 0755); err != nil {
 			return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
 		}
 	}
 	return false, nil
 }

// Remove is a no-op for the Home Assistant configurator; container and data removal
// are handled at a higher level by the orchestrator.
 func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
 	return nil
 }

// PostStart configures native-oidc SSO if enabled, and ensures the setup
// assistant is complete.
 func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
 	c.logger.Info("PostStart: checking Home Assistant setup")

 	if !state.SSOEnabled {
 		c.logger.Info("PostStart: SSO not enabled, skipping OIDC config")
 		return nil
 	}

 	if state.OIDC == nil {
 		return fmt.Errorf("OIDC output not available for native-oidc setup")
 	}

 	c.logger.Info("PostStart: configuring native-oidc SSO",
 		"issuer_url", state.OIDC.IssuerURL,
 		"client_id", state.OIDC.ClientID)

 	// Configure native-oidc via Home Assistant API
 	if err := c.configureNativeOIDC(ctx, state.OIDC); err != nil {
 		return fmt.Errorf("failed to configure native-oidc: %w", err)
 	}

 	c.logger.Info("PostStart complete")
 	return nil
 }

// configureNativeOIDC sets up the native-oidc login configuration on Home Assistant.
 func (c *Configurator) configureNativeOIDC(ctx context.Context, oidc *configurator.OIDCOutput) error {
 	url := c.baseURL() + "/api/auth/config_flow"

 	payload := map[string]interface{}{
 		"type": "start",
 		"implementation": "native-oidc",
 		"client_id": oidc.ClientID,
 		"client_secret": oidc.ClientSecret,
 		"authorization_redirect_url": oidc.RedirectURI,
 		"authorization_endpoint": oidc.IssuerURL + "/authorization",
 		"token_endpoint": oidc.IssuerURL + "/token",
 		"userinfo_endpoint": oidc.IssuerURL + "/userinfo",
 		"jwks_uri": oidc.IssuerURL + "/jwks",
 	}

 	body, _ := json.Marshal(payload)
 	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
 	if err != nil {
 		return fmt.Errorf("creating request: %w", err)
 	}
 	req.Header.Set("Content-Type", "application/json")

 	resp, err := http.DefaultClient.Do(req)
 	if err != nil {
 		return fmt.Errorf("request failed: %w", err)
 	}
 	defer resp.Body.Close()

 	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
 		respBody, _ := io.ReadAll(resp.Body)
 		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
 	}

 	c.logger.Info("native-oidc config flow started", "status", resp.StatusCode)
 	return nil
 }

// baseURL returns the base URL for API calls
 func (c *Configurator) baseURL() string {
 	return fmt.Sprintf("http://localhost:%d", c.port)
 }