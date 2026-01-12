package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
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
		Name: "test",
		SSO:  catalog.SSO{Strategy: ""},
	}
	assert.False(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_NoneStrategy(t *testing.T) {
	app := &catalog.App{
		Name: "test",
		SSO:  catalog.SSO{Strategy: "none"},
	}
	assert.False(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_NativeOIDC(t *testing.T) {
	app := &catalog.App{
		Name: "miniflux",
		SSO:  catalog.SSO{Strategy: "native-oidc"},
	}
	assert.True(t, shouldCleanupAuthentik(app))
}

func TestShouldCleanupAuthentik_ForwardAuth(t *testing.T) {
	app := &catalog.App{
		Name: "adguard-home",
		SSO:  catalog.SSO{Strategy: "forward-auth"},
	}
	assert.True(t, shouldCleanupAuthentik(app))
}

// ============================================================================
// buildTransactionWithApp Tests
// ============================================================================

func TestBuildTransactionWithApp_NilCurrent(t *testing.T) {
	integrations := map[string]string{"database": "postgres"}

	tx := buildTransactionWithApp(nil, "miniflux", integrations)

	assert.NotNil(t, tx)
	assert.Len(t, tx.Apps, 2) // miniflux + postgres

	app := tx.Apps["miniflux"]
	assert.True(t, app.Enabled)
	assert.Equal(t, "postgres", app.Integrations["database"])

	dep := tx.Apps["postgres"]
	assert.True(t, dep.Enabled)
}

func TestBuildTransactionWithApp_PreservesExisting(t *testing.T) {
	current := &nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"redis": {Name: "redis", Enabled: true},
		},
	}

	tx := buildTransactionWithApp(current, "miniflux", nil)

	assert.Len(t, tx.Apps, 2) // redis + miniflux

	// Redis should be preserved
	redis := tx.Apps["redis"]
	assert.True(t, redis.Enabled)

	// Miniflux should be added
	miniflux := tx.Apps["miniflux"]
	assert.True(t, miniflux.Enabled)
}

func TestBuildTransactionWithApp_UpdatesExisting(t *testing.T) {
	current := &nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"miniflux": {Name: "miniflux", Enabled: false},
		},
	}
	integrations := map[string]string{"database": "postgres"}

	tx := buildTransactionWithApp(current, "miniflux", integrations)

	app := tx.Apps["miniflux"]
	assert.True(t, app.Enabled) // Should be enabled now
	assert.Equal(t, "postgres", app.Integrations["database"])
}

func TestBuildTransactionWithApp_AddsDependencies(t *testing.T) {
	integrations := map[string]string{
		"database":        "postgres",
		"download-client": "qbittorrent",
	}

	tx := buildTransactionWithApp(nil, "radarr", integrations)

	assert.Len(t, tx.Apps, 3) // radarr + postgres + qbittorrent

	assert.True(t, tx.Apps["postgres"].Enabled)
	assert.True(t, tx.Apps["qbittorrent"].Enabled)
}

func TestBuildTransactionWithApp_DependencyAlreadyExists(t *testing.T) {
	current := &nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"postgres": {
				Name:         "postgres",
				Enabled:      true,
				Integrations: map[string]string{"some": "config"},
			},
		},
	}
	integrations := map[string]string{"database": "postgres"}

	tx := buildTransactionWithApp(current, "miniflux", integrations)

	// Postgres should retain its original config
	postgres := tx.Apps["postgres"]
	assert.Equal(t, "config", postgres.Integrations["some"])
}

// ============================================================================
// buildTransactionDisablingApp Tests
// ============================================================================

func TestBuildTransactionDisablingApp_NilCurrent(t *testing.T) {
	tx := buildTransactionDisablingApp(nil, "qbittorrent")

	assert.NotNil(t, tx)
	assert.Empty(t, tx.Apps)
}

func TestBuildTransactionDisablingApp_DisablesTarget(t *testing.T) {
	current := &nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"qbittorrent": {Name: "qbittorrent", Enabled: true},
			"radarr":      {Name: "radarr", Enabled: true},
		},
	}

	tx := buildTransactionDisablingApp(current, "qbittorrent")

	assert.False(t, tx.Apps["qbittorrent"].Enabled)
	assert.True(t, tx.Apps["radarr"].Enabled) // Others unchanged
}

func TestBuildTransactionDisablingApp_AppNotInCurrent(t *testing.T) {
	current := &nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			"radarr": {Name: "radarr", Enabled: true},
		},
	}

	tx := buildTransactionDisablingApp(current, "qbittorrent")

	// Only radarr should exist
	assert.Len(t, tx.Apps, 1)
	assert.True(t, tx.Apps["radarr"].Enabled)
}
