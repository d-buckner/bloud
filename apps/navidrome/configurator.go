package navidrome

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

// Configurator handles Navidrome configuration
type Configurator struct {
	Port int
}

// NewConfigurator creates a new Navidrome configurator
func NewConfigurator(port int) *Configurator {
	if port == 0 {
		port = 4533
	}
	return &Configurator{Port: port}
}

func (c *Configurator) Name() string {
	return "navidrome"
}

// PreStart creates the required data and music directories before the container starts.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) error {
	dirs := []string{
		filepath.Join(state.DataPath, "data"),
		filepath.Join(state.BloudDataPath, "media", "music"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// HealthCheck waits for Navidrome to be ready
func (c *Configurator) HealthCheck(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/ping", c.Port)
	return configurator.WaitForHTTP(ctx, url, 60*time.Second)
}

// PostStart is a no-op for Navidrome; all runtime config is done via environment variables.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
	return nil
}
