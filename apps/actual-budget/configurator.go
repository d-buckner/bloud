package actualbudget

import (
	"context"
	"fmt"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles Actual Budget configuration
type Configurator struct {
	port               int
	authentikPort      int
	openidDiscoveryURL string
}

// NewConfigurator creates a new Actual Budget configurator
func NewConfigurator(port, authentikPort int) *Configurator {
	return &Configurator{
		port:          port,
		authentikPort: authentikPort,
		// OpenID discovery URL - uses internal Authentik port, not Traefik
		openidDiscoveryURL: fmt.Sprintf("http://localhost:%d/application/o/actual-budget/.well-known/openid-configuration", authentikPort),
	}
}

// Name returns the app name
func (c *Configurator) Name() string {
	return "actual-budget"
}

// PreStart waits for Authentik's OpenID endpoint if SSO is enabled
// This ensures Actual Budget can connect to OpenID on startup
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	// Check if SSO integration is enabled
	if _, hasSSO := state.Integrations["sso"]; !hasSSO {
		return nil
	}

	// Wait for Authentik's OpenID discovery endpoint to be available
	// Use a longer timeout since Authentik can take a while to start
	timeout := 120 * time.Second
	if err := configurator.WaitForHTTP(ctx, c.openidDiscoveryURL, timeout); err != nil {
		return fmt.Errorf("authentik OpenID endpoint not ready: %w", err)
	}

	return nil
}

// HealthCheck waits for Actual Budget to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/", c.port)
	return configurator.WaitForHTTPWithAuth(ctx, url, configurator.DefaultHealthCheckTimeout)
}

// PostStart handles post-startup configuration
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	// No post-start configuration needed for Actual Budget
	return nil
}
