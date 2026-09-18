// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
)

// runInitSecrets handles the "init-secrets" subcommand
// This generates the secrets.json file if it doesn't exist.
// Should be called before starting any app containers that need secrets.
//
// Usage:
//
//	host-agent init-secrets [data-dir]
//
// If data-dir is not provided, defaults to ~/.local/share/bloud
func runInitSecrets(args []string) int {
	dataDir, err := resolveDataDir(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine home directory: %v\n", err)
		return 1
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

// resolveDataDir returns the data directory, defaulting to ~/.local/share/bloud
// when no explicit path is provided.
func resolveDataDir(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".local", "share", "bloud"), nil
}

// runToken handles the "token" subcommand: it prints the API bearer token
// stored in the runtime's secrets.json to stdout. The CLI and the e2e
// lifecycle use this to authenticate local (loopback/trusted-net) calls to
// the host-agent API now that loopback origin alone no longer grants admin.
//
// Usage:
//
//	host-agent token [data-dir]
//
// If data-dir is not provided, defaults to ~/.local/share/bloud.
func runToken(args []string) int {
	dataDir, err := resolveDataDir(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine home directory: %v\n", err)
		return 1
	}

	secretsPath := filepath.Join(dataDir, "secrets.json")
	if _, err := os.Stat(secretsPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: no secrets at %s (run host-agent init-secrets first)\n", secretsPath)
		return 1
	}

	mgr := secrets.NewManager(secretsPath)
	if err := mgr.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load secrets: %v\n", err)
		return 1
	}

	token := mgr.GetAPIToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: secrets have no apiToken (re-init secrets)")
		return 1
	}

	fmt.Println(token)
	return 0
}
