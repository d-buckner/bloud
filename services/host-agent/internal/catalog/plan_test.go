// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"testing"
)

func buildTestGraph() *AppGraph {
	apps := []*App{
		{CatalogID: "qbittorrent"},
		{CatalogID: "deluge"},
		{CatalogID: "jellyfin"},
		{CatalogID: "plex"},
		{
			CatalogID: "radarr",
			Integrations: map[string]Integration{
				"downloadClient": {
					Required: true,
					Multi:    false,
					Compatible: []CompatibleApp{
						{App: "qbittorrent", Default: true},
						{App: "deluge"},
					},
				},
			},
		},
		{
			CatalogID: "sonarr",
			Integrations: map[string]Integration{
				"downloadClient": {
					Required: true,
					Multi:    false,
					Compatible: []CompatibleApp{
						{App: "qbittorrent", Default: true},
					},
				},
			},
		},
		{
			CatalogID: "jellyseerr",
			Integrations: map[string]Integration{
				"mediaServer": {
					Required: true,
					Multi:    false,
					Compatible: []CompatibleApp{
						{App: "jellyfin", Default: true},
						{App: "plex"},
					},
				},
				"pvr": {
					Required: true,
					Multi:    true,
					Compatible: []CompatibleApp{
						{App: "radarr", Category: "movies"},
						{App: "sonarr", Category: "tv"},
					},
				},
			},
		},
	}
	return NewGraph(apps)
}

// A required contract with nothing installed resolves to its declared default
// provider, which installing the app installs with it. There is no choice: the
// default is the only candidate the loader permits for a required contract.
func TestPlanInstall_MissingRequiredDependency(t *testing.T) {
	g := buildTestGraph()
	// No download clients installed

	plan, err := g.PlanInstall("radarr")
	if err != nil {
		t.Fatal(err)
	}

	if !plan.CanInstall {
		t.Error("expected CanInstall true (the required provider installs with the app)")
	}
	if len(plan.RequiredProviders) != 1 {
		t.Fatalf("expected 1 required provider, got %d", len(plan.RequiredProviders))
	}
	if plan.RequiredProviders[0].Integration != "downloadClient" {
		t.Errorf("expected downloadClient, got %s", plan.RequiredProviders[0].Integration)
	}
	if plan.RequiredProviders[0].Source != "qbittorrent" {
		t.Errorf("expected the default qbittorrent, got %s", plan.RequiredProviders[0].Source)
	}
	if len(plan.AutoConfig) != 0 {
		t.Errorf("expected no wired providers yet, got %d", len(plan.AutoConfig))
	}
}

func TestPlanInstall_AutoConfigWhenOneInstalled(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"qbittorrent"})

	plan, err := g.PlanInstall("radarr")
	if err != nil {
		t.Fatal(err)
	}

	if !plan.CanInstall {
		t.Error("expected CanInstall true")
	}
	if len(plan.RequiredProviders) != 0 {
		t.Errorf("expected no required providers, got %d", len(plan.RequiredProviders))
	}
	if len(plan.AutoConfig) != 1 {
		t.Fatalf("expected 1 wired provider, got %d", len(plan.AutoConfig))
	}
	if plan.AutoConfig[0].Source != "qbittorrent" {
		t.Errorf("expected qbittorrent source, got %s", plan.AutoConfig[0].Source)
	}
}

// The set model: when both compatible providers are installed, both wire.
// Nothing is picked, and the required flag does not trim the set.
func TestPlanInstall_WiresEveryInstalledProvider(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"qbittorrent", "deluge"})

	plan, err := g.PlanInstall("radarr")
	if err != nil {
		t.Fatal(err)
	}

	if len(plan.RequiredProviders) != 0 {
		t.Fatalf("expected no required providers, got %d", len(plan.RequiredProviders))
	}
	if len(plan.AutoConfig) != 2 {
		t.Fatalf("expected both installed providers to wire, got %d", len(plan.AutoConfig))
	}
}

func TestPlanInstall_AutoConfigAllWhenMultiTrue(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"jellyfin", "radarr", "sonarr"})

	plan, err := g.PlanInstall("jellyseerr")
	if err != nil {
		t.Fatal(err)
	}

	// jellyseerr.pvr is a set: both installed PVRs wire, plus the installed
	// media server.
	pvrConfigs := 0
	for _, cfg := range plan.AutoConfig {
		if cfg.Integration == "pvr" {
			pvrConfigs++
		}
	}
	if pvrConfigs != 2 {
		t.Errorf("expected 2 pvr wired providers, got %d", pvrConfigs)
	}
}

func TestPlanInstall_FindsDependents(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"qbittorrent", "jellyfin", "jellyseerr"})

	plan, err := g.PlanInstall("radarr")
	if err != nil {
		t.Fatal(err)
	}

	// jellyseerr should be listed as dependent (it will integrate with radarr)
	if len(plan.Dependents) != 1 {
		t.Fatalf("expected 1 dependent, got %d", len(plan.Dependents))
	}
	if plan.Dependents[0].Target != "jellyseerr" {
		t.Errorf("expected jellyseerr, got %s", plan.Dependents[0].Target)
	}
}

func TestPlanRemove_BlockedWhenRequired(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"qbittorrent", "radarr"})

	plan, err := g.PlanRemove("qbittorrent")
	if err != nil {
		t.Fatal(err)
	}

	if plan.CanRemove {
		t.Error("expected CanRemove false")
	}
	if len(plan.Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(plan.Blockers))
	}
}

func TestPlanRemove_AllowedWithAlternative(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"qbittorrent", "deluge", "radarr"})

	plan, err := g.PlanRemove("qbittorrent")
	if err != nil {
		t.Fatal(err)
	}

	// Can remove because deluge is an alternative
	if !plan.CanRemove {
		t.Error("expected CanRemove true")
	}
	if len(plan.WillUnconfigure) != 1 {
		t.Fatalf("expected 1 unconfigure, got %d", len(plan.WillUnconfigure))
	}
}
