package nixgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerator_GenerateConfig(t *testing.T) {
	gen := NewGenerator("/etc/bloud/apps.nix", "/etc/bloud/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {
				Name:    "radarr",
				Enabled: true,
				Integrations: map[string]string{
					"downloadClient": "qbittorrent",
				},
			},
			"qbittorrent": {
				Name:    "qbittorrent",
				Enabled: true,
			},
		},
	}

	config := gen.generateConfig(tx)

	// Should include app enable statements
	// Note: imports are handled by the main NixOS config, not generated here
	assert.Contains(t, config, "bloud.apps.qbittorrent.enable = true;")
	assert.Contains(t, config, "bloud.apps.radarr.enable = true;")

	// Should be valid Nix syntax (basic check)
	assert.True(t, strings.HasPrefix(config, "# Generated"))
	assert.True(t, strings.Contains(config, "{ config, lib, pkgs, ... }:"))
}

func TestGenerator_Apply(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apps.nix")

	gen := NewGenerator(configPath, tmpDir)

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"jellyfin": {
				Name:    "jellyfin",
				Enabled: true,
			},
		},
	}

	err := gen.Apply(tx)
	require.NoError(t, err)

	// File should exist
	_, err = os.Stat(configPath)
	require.NoError(t, err)

	// File should contain expected content
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), "bloud.apps.jellyfin.enable = true;")
}

func TestGenerator_Diff(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	current := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {
				Name:    "radarr",
				Enabled: true,
				Integrations: map[string]string{
					"downloadClient": "transmission",
				},
			},
		},
	}

	proposed := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {
				Name:    "radarr",
				Enabled: true,
				Integrations: map[string]string{
					"downloadClient": "qbittorrent",
				},
			},
			"qbittorrent": {
				Name:    "qbittorrent",
				Enabled: true,
			},
		},
	}

	diff := gen.Diff(current, proposed)

	assert.Contains(t, diff, "Install qbittorrent")
	assert.Contains(t, diff, "Update radarr.downloadClient")
}

func TestGenerator_DiffNoChanges(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {
				Name:    "radarr",
				Enabled: true,
			},
		},
	}

	diff := gen.Diff(tx, tx)
	assert.Equal(t, "No changes", diff)
}

func TestGenerator_DeterministicOutput(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"zebra": {Name: "zebra", Enabled: true},
			"alpha": {Name: "alpha", Enabled: true},
			"beta":  {Name: "beta", Enabled: true},
		},
	}

	// Generate twice and compare
	output1 := gen.generateConfig(tx)
	output2 := gen.generateConfig(tx)

	assert.Equal(t, output1, output2, "Output should be deterministic")

	// Should be sorted alphabetically
	alphaIdx := strings.Index(output1, "bloud.apps.alpha.enable")
	betaIdx := strings.Index(output1, "bloud.apps.beta.enable")
	zebraIdx := strings.Index(output1, "bloud.apps.zebra.enable")

	assert.True(t, alphaIdx < betaIdx, "alpha should come before beta")
	assert.True(t, betaIdx < zebraIdx, "beta should come before zebra")
}

func TestGenerator_DisabledApps(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"enabled": {
				Name:    "enabled",
				Enabled: true,
			},
			"disabled": {
				Name:    "disabled",
				Enabled: false,
			},
		},
	}

	config := gen.generateConfig(tx)

	// Should only include enabled app
	assert.Contains(t, config, "bloud.apps.enabled.enable = true;")
	assert.NotContains(t, config, "bloud.apps.disabled")
}

func TestGenerator_AppsWithoutIntegrations(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"postgres": {
				Name:         "postgres",
				Enabled:      true,
				Integrations: nil, // No integrations
			},
			"miniflux": {
				Name:    "miniflux",
				Enabled: true,
				Integrations: map[string]string{
					"database": "postgres",
				},
			},
		},
	}

	config := gen.generateConfig(tx)

	// Both apps should have enable = true (simplified format)
	assert.Contains(t, config, "bloud.apps.postgres.enable = true;")
	assert.Contains(t, config, "bloud.apps.miniflux.enable = true;")

	// Count occurrences of "enable = true" - should be 2
	enableCount := strings.Count(config, "enable = true")
	assert.Equal(t, 2, enableCount, "Both apps should have enable = true")
}

func TestGenerator_StatePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apps.nix")

	gen := NewGenerator(configPath, tmpDir)

	// Initially should return empty transaction
	initial, err := gen.LoadCurrent()
	require.NoError(t, err)
	assert.Empty(t, initial.Apps)

	// Apply a transaction
	tx := &Transaction{
		Apps: map[string]AppConfig{
			"miniflux": {
				Name:    "miniflux",
				Enabled: true,
				Integrations: map[string]string{
					"database": "postgres",
				},
			},
			"postgres": {
				Name:    "postgres",
				Enabled: true,
			},
		},
	}

	err = gen.Apply(tx)
	require.NoError(t, err)

	// Load should return the saved state
	loaded, err := gen.LoadCurrent()
	require.NoError(t, err)

	assert.Len(t, loaded.Apps, 2)
	assert.True(t, loaded.Apps["miniflux"].Enabled)
	assert.True(t, loaded.Apps["postgres"].Enabled)
	assert.Equal(t, "postgres", loaded.Apps["miniflux"].Integrations["database"])

	// State file should exist
	statePath := strings.TrimSuffix(configPath, ".nix") + "-state.json"
	_, err = os.Stat(statePath)
	require.NoError(t, err)
}

func TestGenerator_LoadCurrentInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apps.nix")
	statePath := filepath.Join(tmpDir, "apps-state.json")

	// Write invalid JSON to state file
	err := os.WriteFile(statePath, []byte("not valid json{{{"), 0644)
	require.NoError(t, err)

	gen := NewGenerator(configPath, tmpDir)
	_, err = gen.LoadCurrent()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse state file")
}

func TestGenerator_LoadCurrentNullApps(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "apps.nix")
	statePath := filepath.Join(tmpDir, "apps-state.json")

	// Write JSON with null apps
	err := os.WriteFile(statePath, []byte(`{"Apps": null}`), 0644)
	require.NoError(t, err)

	gen := NewGenerator(configPath, tmpDir)
	tx, err := gen.LoadCurrent()

	require.NoError(t, err)
	assert.NotNil(t, tx.Apps, "Apps should be initialized even if null in JSON")
}

func TestGenerator_Preview(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"jellyfin": {Name: "jellyfin", Enabled: true},
		},
	}

	preview := gen.Preview(tx)

	assert.Contains(t, preview, "# Generated by Bloud")
	assert.Contains(t, preview, "bloud.apps.jellyfin.enable = true;")
}

func TestGenerator_DiffRemoval(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	current := &Transaction{
		Apps: map[string]AppConfig{
			"radarr":      {Name: "radarr", Enabled: true},
			"qbittorrent": {Name: "qbittorrent", Enabled: true},
		},
	}

	proposed := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {Name: "radarr", Enabled: true},
			// qbittorrent removed
		},
	}

	diff := gen.Diff(current, proposed)

	assert.Contains(t, diff, "- Remove qbittorrent")
	assert.NotContains(t, diff, "radarr")
}

func TestGenerator_DiffDisabling(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	current := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {Name: "radarr", Enabled: true},
		},
	}

	proposed := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {Name: "radarr", Enabled: false}, // Disabled, not removed
		},
	}

	diff := gen.Diff(current, proposed)

	assert.Contains(t, diff, "- Remove radarr")
}

func TestGenerator_DiffAddingIntegration(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	current := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {
				Name:         "radarr",
				Enabled:      true,
				Integrations: map[string]string{},
			},
		},
	}

	proposed := &Transaction{
		Apps: map[string]AppConfig{
			"radarr": {
				Name:    "radarr",
				Enabled: true,
				Integrations: map[string]string{
					"downloadClient": "qbittorrent",
				},
			},
		},
	}

	diff := gen.Diff(current, proposed)

	assert.Contains(t, diff, "+ Configure radarr.downloadClient = qbittorrent")
}

func TestGenerator_EmptyTransaction(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	tx := &Transaction{
		Apps: map[string]AppConfig{},
	}

	config := gen.generateConfig(tx)

	// Should still be valid Nix
	assert.Contains(t, config, "# Generated by Bloud")
	assert.Contains(t, config, "{ config, lib, pkgs, ... }:")
	assert.Contains(t, config, "{\n}\n")
}

func TestGenerator_GetModulePath(t *testing.T) {
	gen := NewGenerator("/tmp/apps.nix", "/tmp/nixos")

	path := gen.getModulePath("jellyfin")

	assert.Equal(t, "../../nixos/apps/jellyfin.nix", path)
}

func TestGenerator_ApplyCreatesDirIfNeeded(t *testing.T) {
	tmpDir := t.TempDir()
	// Nested path that doesn't exist
	configPath := filepath.Join(tmpDir, "nested", "deep", "apps.nix")

	gen := NewGenerator(configPath, tmpDir)

	tx := &Transaction{
		Apps: map[string]AppConfig{
			"test": {Name: "test", Enabled: true},
		},
	}

	err := gen.Apply(tx)
	require.NoError(t, err)

	// Directory should have been created
	_, err = os.Stat(filepath.Join(tmpDir, "nested", "deep"))
	require.NoError(t, err)
}
