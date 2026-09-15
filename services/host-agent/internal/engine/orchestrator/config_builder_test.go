// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
)

// ============================================================================
// buildIntegrationConfig Tests
// ============================================================================

func TestBuildIntegrationConfig_EmptyInputs(t *testing.T) {
	config := buildIntegrationConfig(nil, nil, nil)

	assert.NotNil(t, config)
	assert.Empty(t, config)
}

func TestBuildIntegrationConfig_UserChoicesOnly(t *testing.T) {
	userChoices := map[string]string{
		"download-client": "qbittorrent",
		"media-server":    "jellyfin",
	}

	config := buildIntegrationConfig(userChoices, nil, nil)

	assert.Equal(t, "qbittorrent", config["download-client"])
	assert.Equal(t, "jellyfin", config["media-server"])
	assert.Len(t, config, 2)
}

func TestBuildIntegrationConfig_AutoConfigOnly(t *testing.T) {
	autoConfig := []catalog.ConfigTask{
		{Integration: "database", Source: "postgres"},
		{Integration: "cache", Source: "redis"},
	}

	config := buildIntegrationConfig(nil, autoConfig, nil)

	assert.Equal(t, "postgres", config["database"])
	assert.Equal(t, "redis", config["cache"])
	assert.Len(t, config, 2)
}

func TestBuildIntegrationConfig_RequiredChoiceUsesRecommended(t *testing.T) {
	choices := []catalog.IntegrationChoice{
		{
			Integration: "download-client",
			Required:    true,
			Recommended: "qbittorrent",
		},
	}

	config := buildIntegrationConfig(nil, nil, choices)

	assert.Equal(t, "qbittorrent", config["download-client"])
}

func TestBuildIntegrationConfig_OptionalChoiceSkipped(t *testing.T) {
	choices := []catalog.IntegrationChoice{
		{
			Integration: "media-server",
			Required:    false,
			Recommended: "jellyfin",
		},
	}

	config := buildIntegrationConfig(nil, nil, choices)

	_, exists := config["media-server"]
	assert.False(t, exists, "optional choice should not be added")
}

func TestBuildIntegrationConfig_AutoConfigOverridesUserChoice(t *testing.T) {
	userChoices := map[string]string{
		"database": "mariadb", // User tried to choose mariadb
	}
	autoConfig := []catalog.ConfigTask{
		{Integration: "database", Source: "postgres"}, // Auto-config requires postgres
	}

	config := buildIntegrationConfig(userChoices, autoConfig, nil)

	// Auto-config should override user choice - it's required for app functionality
	assert.Equal(t, "postgres", config["database"])
}

func TestBuildIntegrationConfig_UserChoiceOverridesRecommended(t *testing.T) {
	userChoices := map[string]string{
		"download-client": "deluge",
	}
	choices := []catalog.IntegrationChoice{
		{
			Integration: "download-client",
			Required:    true,
			Recommended: "qbittorrent",
		},
	}

	config := buildIntegrationConfig(userChoices, nil, choices)

	assert.Equal(t, "deluge", config["download-client"])
}

func TestBuildIntegrationConfig_AllSourcesCombined(t *testing.T) {
	userChoices := map[string]string{
		"download-client": "deluge",
	}
	autoConfig := []catalog.ConfigTask{
		{Integration: "database", Source: "postgres"},
	}
	choices := []catalog.IntegrationChoice{
		{
			Integration: "cache",
			Required:    true,
			Recommended: "redis",
		},
	}

	config := buildIntegrationConfig(userChoices, autoConfig, choices)

	assert.Equal(t, "deluge", config["download-client"])
	assert.Equal(t, "postgres", config["database"])
	assert.Equal(t, "redis", config["cache"])
	assert.Len(t, config, 3)
}

func TestBuildIntegrationConfig_RequiredWithEmptyRecommended(t *testing.T) {
	choices := []catalog.IntegrationChoice{
		{
			Integration: "download-client",
			Required:    true,
			Recommended: "", // No recommendation
		},
	}

	config := buildIntegrationConfig(nil, nil, choices)

	// Should not add anything if Recommended is empty
	_, exists := config["download-client"]
	assert.False(t, exists)
}

// ============================================================================
// shouldCleanupAuthentik Tests
// ============================================================================

func TestShouldCleanupAuthentik_NilApp(t *testing.T) {
	assert.False(t, shouldCleanupAuthentik(nil))
}

func TestShouldCleanupAuthentik_EmptyStrategy(t *testing.T) {
	app := &catalog.App{
		CatalogID: "test",
		SSO:       catalog.SSO{Strategy: ""},
	}
	assert.False(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_NoneStrategy(t *testing.T) {
	app := &catalog.App{
		CatalogID: "test",
		SSO:       catalog.SSO{Strategy: "none"},
	}
	assert.False(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_NativeOIDC(t *testing.T) {
	app := &catalog.App{
		CatalogID: "miniflux",
		SSO:       catalog.SSO{Strategy: "native-oidc"},
	}
	assert.True(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_ForwardAuth(t *testing.T) {
	app := &catalog.App{
		CatalogID: "adguard-home",
		SSO:       catalog.SSO{Strategy: "forward-auth"},
	}
	assert.True(t, shouldCleanupAuthentik(app))
}
