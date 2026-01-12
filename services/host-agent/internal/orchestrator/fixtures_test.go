package orchestrator

import (
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// Test app fixtures

func fixtureQBittorrent() *catalog.App {
	return &catalog.App{
		Name:        "qbittorrent",
		DisplayName: "qBittorrent",
		Port:        8180,
		Category:    "media",
		IsSystem:    false,
	}
}

func fixtureRadarr() *catalog.App {
	return &catalog.App{
		Name:        "radarr",
		DisplayName: "Radarr",
		Port:        7878,
		Category:    "media",
		IsSystem:    false,
		HealthCheck: catalog.HealthCheck{
			Path:     "/health",
			Interval: 2,
			Timeout:  30,
		},
	}
}

func fixtureSonarr() *catalog.App {
	return &catalog.App{
		Name:        "sonarr",
		DisplayName: "Sonarr",
		Port:        8989,
		Category:    "media",
		IsSystem:    false,
		HealthCheck: catalog.HealthCheck{
			Path:     "/health",
			Interval: 2,
			Timeout:  30,
		},
	}
}

func fixtureMiniflux() *catalog.App {
	return &catalog.App{
		Name:        "miniflux",
		DisplayName: "Miniflux",
		Port:        8280,
		Category:    "productivity",
		IsSystem:    false,
		SSO: catalog.SSO{
			Strategy:     "native-oidc",
			CallbackPath: "/oauth2/oidc/callback",
		},
		HealthCheck: catalog.HealthCheck{
			Path:     "/healthcheck",
			Interval: 2,
			Timeout:  30,
		},
	}
}

func fixtureActualBudget() *catalog.App {
	return &catalog.App{
		Name:        "actual-budget",
		DisplayName: "Actual Budget",
		Port:        5006,
		Category:    "productivity",
		IsSystem:    false,
		SSO: catalog.SSO{
			Strategy:     "native-oidc",
			CallbackPath: "/openid/callback",
		},
	}
}

func fixtureAdguardHome() *catalog.App {
	return &catalog.App{
		Name:        "adguard-home",
		DisplayName: "AdGuard Home",
		Port:        3000,
		Category:    "network",
		IsSystem:    false,
		SSO: catalog.SSO{
			Strategy: "forward-auth",
		},
	}
}

func fixturePostgres() *catalog.App {
	return &catalog.App{
		Name:        "postgres",
		DisplayName: "PostgreSQL",
		Port:        5432,
		Category:    "infrastructure",
		IsSystem:    true,
	}
}

func fixtureRedis() *catalog.App {
	return &catalog.App{
		Name:        "redis",
		DisplayName: "Redis",
		Port:        6379,
		Category:    "infrastructure",
		IsSystem:    true,
	}
}

func fixtureAuthentik() *catalog.App {
	return &catalog.App{
		Name:        "authentik",
		DisplayName: "Authentik",
		Port:        9000,
		Category:    "infrastructure",
		IsSystem:    true,
	}
}

// Test installed app fixtures

func fixtureInstalledApp(name string, status string) *store.InstalledApp {
	return &store.InstalledApp{
		ID:                1,
		Name:              name,
		DisplayName:       name,
		Status:            status,
		InstalledAt:       time.Now(),
		UpdatedAt:         time.Now(),
		IntegrationConfig: make(map[string]string),
	}
}

func fixtureInstalledAppWithPort(name string, status string, port int) *store.InstalledApp {
	app := fixtureInstalledApp(name, status)
	app.Port = port
	return app
}

func fixtureInstalledAppWithIntegrations(name string, status string, integrations map[string]string) *store.InstalledApp {
	app := fixtureInstalledApp(name, status)
	app.IntegrationConfig = integrations
	return app
}

// Test install plan fixtures

func fixtureInstallPlanCanInstall(app string) *catalog.InstallPlan {
	return &catalog.InstallPlan{
		App:        app,
		CanInstall: true,
		Choices:    []catalog.IntegrationChoice{},
		AutoConfig: []catalog.ConfigTask{},
		Dependents: []catalog.ConfigTask{},
	}
}

func fixtureInstallPlanBlocked(app string, blockers []string) *catalog.InstallPlan {
	return &catalog.InstallPlan{
		App:        app,
		CanInstall: false,
		Blockers:   blockers,
	}
}

func fixtureInstallPlanWithAutoConfig(app string, autoConfig []catalog.ConfigTask) *catalog.InstallPlan {
	return &catalog.InstallPlan{
		App:        app,
		CanInstall: true,
		Choices:    []catalog.IntegrationChoice{},
		AutoConfig: autoConfig,
		Dependents: []catalog.ConfigTask{},
	}
}

func fixtureInstallPlanWithChoices(app string, choices []catalog.IntegrationChoice) *catalog.InstallPlan {
	return &catalog.InstallPlan{
		App:        app,
		CanInstall: true,
		Choices:    choices,
		AutoConfig: []catalog.ConfigTask{},
		Dependents: []catalog.ConfigTask{},
	}
}

func fixtureInstallPlanWithDependents(app string, dependents []catalog.ConfigTask) *catalog.InstallPlan {
	return &catalog.InstallPlan{
		App:        app,
		CanInstall: true,
		Choices:    []catalog.IntegrationChoice{},
		AutoConfig: []catalog.ConfigTask{},
		Dependents: dependents,
	}
}

// Test remove plan fixtures

func fixtureRemovePlanCanRemove(app string) *catalog.RemovePlan {
	return &catalog.RemovePlan{
		App:             app,
		CanRemove:       true,
		Blockers:        []string{},
		WillUnconfigure: []string{},
	}
}

func fixtureRemovePlanBlocked(app string, blockers []string) *catalog.RemovePlan {
	return &catalog.RemovePlan{
		App:       app,
		CanRemove: false,
		Blockers:  blockers,
	}
}

func fixtureRemovePlanWithUnconfigure(app string, willUnconfigure []string) *catalog.RemovePlan {
	return &catalog.RemovePlan{
		App:             app,
		CanRemove:       true,
		Blockers:        []string{},
		WillUnconfigure: willUnconfigure,
	}
}

// Test transaction fixtures

func fixtureEmptyTransaction() *nixgen.Transaction {
	return &nixgen.Transaction{
		Apps: make(map[string]nixgen.AppConfig),
	}
}

func fixtureTransactionWithApp(appName string) *nixgen.Transaction {
	return &nixgen.Transaction{
		Apps: map[string]nixgen.AppConfig{
			appName: {
				Name:         appName,
				Enabled:      true,
				Integrations: make(map[string]string),
			},
		},
	}
}

func fixtureTransactionWithApps(appNames ...string) *nixgen.Transaction {
	tx := &nixgen.Transaction{
		Apps: make(map[string]nixgen.AppConfig),
	}
	for _, name := range appNames {
		tx.Apps[name] = nixgen.AppConfig{
			Name:         name,
			Enabled:      true,
			Integrations: make(map[string]string),
		}
	}
	return tx
}

// Test rebuild result fixtures

func fixtureRebuildSuccess() *nixgen.RebuildResult {
	return &nixgen.RebuildResult{
		Success:  true,
		Output:   "rebuilding...\nactivating...\n",
		Duration: 5 * time.Second,
		Changes:  []string{"starting podman-qbittorrent.service"},
	}
}

func fixtureRebuildFailure(errorMsg string) *nixgen.RebuildResult {
	return &nixgen.RebuildResult{
		Success:      false,
		ErrorMessage: errorMsg,
		Output:       "error: " + errorMsg,
		Duration:     2 * time.Second,
	}
}

// Test config task fixtures

func fixtureConfigTask(target, source, integration string) catalog.ConfigTask {
	return catalog.ConfigTask{
		Target:      target,
		Source:      source,
		Integration: integration,
	}
}

// Test integration choice fixtures

func fixtureIntegrationChoice(integration string, required bool, installed, available []string) catalog.IntegrationChoice {
	choice := catalog.IntegrationChoice{
		Integration: integration,
		Required:    required,
		Installed:   make([]catalog.ChoiceOption, len(installed)),
		Available:   make([]catalog.ChoiceOption, len(available)),
	}

	for i, app := range installed {
		choice.Installed[i] = catalog.ChoiceOption{App: app}
	}
	for i, app := range available {
		choice.Available[i] = catalog.ChoiceOption{App: app}
	}

	return choice
}

// Test app definition fixtures (for AppGraph)

func fixtureAppDefinition(name string) *catalog.AppDefinition {
	return &catalog.AppDefinition{
		Name:         name,
		Integrations: make(map[string]catalog.Integration),
	}
}

func fixtureAppDefinitionWithIntegration(name string, integrations map[string]catalog.Integration) *catalog.AppDefinition {
	return &catalog.AppDefinition{
		Name:         name,
		Integrations: integrations,
	}
}
