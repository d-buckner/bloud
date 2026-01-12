package nixgen

import "context"

// GeneratorInterface defines the interface for generating Nix configuration files.
// This interface enables mocking for testing.
type GeneratorInterface interface {
	// LoadCurrent loads the current app configuration from the state file
	LoadCurrent() (*Transaction, error)

	// Apply applies a transaction by generating Nix config and saving state
	Apply(tx *Transaction) error

	// Preview generates a preview of what the config will look like
	Preview(tx *Transaction) string

	// Diff shows the difference between current and proposed config
	Diff(current, proposed *Transaction) string
}

// RebuilderInterface defines the interface for nixos-rebuild operations.
// This interface enables mocking for testing.
type RebuilderInterface interface {
	// Switch performs a nixos-rebuild switch
	Switch(ctx context.Context) (*RebuildResult, error)

	// Rollback rolls back to the previous generation
	Rollback(ctx context.Context) (*RebuildResult, error)

	// SwitchStream performs a nixos-rebuild switch with streaming output
	SwitchStream(ctx context.Context, events chan<- RebuildEvent)

	// StopUserService stops a systemd user service for an app
	StopUserService(ctx context.Context, appName string) error

	// ReloadAndRestartApps reloads systemd user daemon and restarts all bloud apps
	ReloadAndRestartApps(ctx context.Context) error
}

// Compile-time assertions
var _ GeneratorInterface = (*Generator)(nil)
var _ RebuilderInterface = (*Rebuilder)(nil)
