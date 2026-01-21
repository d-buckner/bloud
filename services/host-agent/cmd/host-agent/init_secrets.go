package main

import (
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/secrets"
)

// runInitSecrets handles the "init-secrets" subcommand
// This generates the secrets.json file if it doesn't exist.
// Should be called BEFORE nixos-rebuild to ensure NixOS can read the secrets.
//
// Usage:
//
//	host-agent init-secrets [data-dir]
//
// If data-dir is not provided, defaults to ~/.local/share/bloud
func runInitSecrets(args []string) int {
	// Determine data directory
	dataDir := ""
	if len(args) > 0 {
		dataDir = args[0]
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine home directory: %v\n", err)
			return 1
		}
		dataDir = filepath.Join(homeDir, ".local", "share", "bloud")
	}

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot create data directory %s: %v\n", dataDir, err)
		return 1
	}

	secretsPath := filepath.Join(dataDir, "secrets.json")

	// Check if secrets already exist
	if _, err := os.Stat(secretsPath); err == nil {
		fmt.Printf("Secrets already exist at %s\n", secretsPath)
		return 0
	}

	// Generate new secrets
	mgr := secrets.NewManager(secretsPath)
	if err := mgr.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to generate secrets: %v\n", err)
		return 1
	}

	fmt.Printf("Generated secrets at %s\n", secretsPath)
	return 0
}
