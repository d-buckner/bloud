package catalog

import (
	"testing"
)

func buildTestGraph() *AppGraph {
	apps := []*AppDefinition{
		{Name: "qbittorrent"},
		{Name: "deluge"},
		{Name: "jellyfin"},
		{Name: "plex"},
		{
			Name: "radarr",
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
			Name: "sonarr",
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
			Name: "jellyseerr",
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

func TestPlanInstall_MissingRequiredDependency(t *testing.T) {
	g := buildTestGraph()
	// No download clients installed

	plan, err := g.PlanInstall("radarr")
	if err != nil {
		t.Fatal(err)
	}

	if !plan.CanInstall {
		t.Error("expected CanInstall true (user can choose to install dependency)")
	}
	if len(plan.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(plan.Choices))
	}
	if plan.Choices[0].Integration != "downloadClient" {
		t.Errorf("expected downloadClient choice, got %s", plan.Choices[0].Integration)
	}
	if plan.Choices[0].Recommended != "qbittorrent" {
		t.Errorf("expected qbittorrent recommended, got %s", plan.Choices[0].Recommended)
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
	if len(plan.Choices) != 0 {
		t.Errorf("expected no choices, got %d", len(plan.Choices))
	}
	if len(plan.AutoConfig) != 1 {
		t.Fatalf("expected 1 auto config, got %d", len(plan.AutoConfig))
	}
	if plan.AutoConfig[0].Source != "qbittorrent" {
		t.Errorf("expected qbittorrent source, got %s", plan.AutoConfig[0].Source)
	}
}

func TestPlanInstall_ChoiceWhenMultipleInstalled(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"qbittorrent", "deluge"})

	plan, err := g.PlanInstall("radarr")
	if err != nil {
		t.Fatal(err)
	}

	// radarr.downloadClient.multi is false, so need to choose
	if len(plan.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(plan.Choices))
	}
	if len(plan.Choices[0].Installed) != 2 {
		t.Errorf("expected 2 installed options, got %d", len(plan.Choices[0].Installed))
	}
}

func TestPlanInstall_AutoConfigAllWhenMultiTrue(t *testing.T) {
	g := buildTestGraph()
	g.SetInstalled([]string{"jellyfin", "radarr", "sonarr"})

	plan, err := g.PlanInstall("jellyseerr")
	if err != nil {
		t.Fatal(err)
	}

	// jellyseerr.pvr.multi is true, so auto-config both
	pvrConfigs := 0
	for _, cfg := range plan.AutoConfig {
		if cfg.Integration == "pvr" {
			pvrConfigs++
		}
	}
	if pvrConfigs != 2 {
		t.Errorf("expected 2 pvr auto configs, got %d", pvrConfigs)
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
