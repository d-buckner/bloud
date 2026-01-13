package actualbudget

import (
	"context"
	"fmt"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles Actual Budget configuration
type Configurator struct {
	port int
}

// NewConfigurator creates a new Actual Budget configurator
func NewConfigurator(port int) *Configurator {
	return &Configurator{
		port: port,
	}
}

// Name returns the app name
func (c *Configurator) Name() string {
	return "actual-budget"
}

// PreStart handles pre-startup configuration
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	// No app-specific pre-start configuration needed
	// SSO wait is handled automatically by the framework
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
