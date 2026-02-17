// Package configurator provides the interface and utilities for app configuration.
// Configurators handle app-specific setup that can't be expressed purely in Nix,
// such as config file generation and API-based configuration.
package configurator

import (
	"context"
)

// Configurator handles app-specific configuration.
// All methods must be idempotent - safe to call repeatedly.
// Configurators run as systemd hooks on every service start:
// - PreStart runs as ExecStartPre (before container)
// - PostStart runs as ExecStartPost (after container healthy)
type Configurator interface {
	// Name returns the app name this configurator handles
	Name() string

	// PreStart runs before the container starts.
	// Use for: config files, directories, certificates, initial setup.
	// Called every reconciliation - must be idempotent.
	PreStart(ctx context.Context, state *AppState) error

	// HealthCheck waits for the app to be ready for configuration.
	// Use for: waiting for web UI, API, database to accept connections.
	// Returns nil when ready, error on timeout.
	HealthCheck(ctx context.Context) error

	// PostStart runs after container is healthy.
	// Use for: API calls, integrations, runtime configuration.
	// Called every reconciliation - must be idempotent.
	PostStart(ctx context.Context, state *AppState) error
}

// AppState contains everything a configurator needs to configure an app.
type AppState struct {
	// Name is the app name (e.g., "qbittorrent", "radarr")
	Name string

	// DataPath is the app's data directory (e.g., ~/.local/share/bloud/qbittorrent)
	DataPath string

	// BloudDataPath is the shared bloud data directory (e.g., ~/.local/share/bloud)
	// Use for shared resources like downloads, movies, tv
	BloudDataPath string

	// Port is the host port the app is exposed on
	Port int

	// Integrations maps integration names to source app names
	// e.g., {"downloadClient": ["qbittorrent"], "indexer": ["prowlarr"]}
	Integrations map[string][]string

	// Options contains app-specific configuration options
	Options map[string]any
}
