// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"strings"
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

// The shipped Hermes pair, asserted against the real catalog rather than a
// synthetic graph. The claim under test is about what those two metadata
// files say, so a synthetic graph would test the planner (already covered)
// and leave the dependency itself unpinned.
func TestPlanInstall_ShippedHermesWebUIRequiresHermes(t *testing.T) {
	g, err := NewLoader(realCatalogDir(t)).LoadGraph()
	if err != nil {
		t.Fatal(err)
	}

	// Nothing installed: the required contract has to surface as a choice
	// naming Hermes as the thing to install, because that is what the
	// orchestrator records and then records as a dependency first.
	g.SetInstalled(nil)
	plan, err := g.PlanInstall("hermes-webui")
	if err != nil {
		t.Fatal(err)
	}

	var choice *IntegrationChoice
	for i := range plan.Choices {
		if plan.Choices[i].Integration == "agentGateway" {
			choice = &plan.Choices[i]
		}
	}
	if choice == nil {
		t.Fatalf("installing hermes-webui produced no agentGateway choice: %+v", plan.Choices)
	}
	if !choice.Required {
		t.Error("the agentGateway choice must be required: the front end has no agent without it")
	}
	if choice.Recommended != "hermes" {
		t.Errorf("recommended provider = %q, want hermes", choice.Recommended)
	}
}

// Removing the agent out from under an installed front end is the case the
// required flag exists to stop. No alternative provider is shipped, so
// there is nothing to fall back to and the removal has to be refused.
func TestPlanRemove_HermesBlockedByInstalledWebUI(t *testing.T) {
	g, err := NewLoader(realCatalogDir(t)).LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	g.SetInstalled([]string{"hermes", "hermes-webui"})

	plan, err := g.PlanRemove("hermes")
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanRemove {
		t.Fatalf("removing Hermes under an installed Hermes Web UI was allowed: %+v", plan)
	}
	var named bool
	for _, b := range plan.Blockers {
		if strings.Contains(b, "hermes-webui") && strings.Contains(b, "agentGateway") {
			named = true
		}
	}
	if !named {
		t.Errorf("blockers do not name the dependent and its contract: %v", plan.Blockers)
	}
}

// With the front end gone the agent is nothing but itself again, and the
// block has to lift. A blocker that outlives its dependent is a blocker that
// can never be cleared.
func TestPlanRemove_HermesAllowedOnceWebUIIsGone(t *testing.T) {
	g, err := NewLoader(realCatalogDir(t)).LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	g.SetInstalled([]string{"hermes"})

	plan, err := g.PlanRemove("hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanRemove {
		t.Errorf("Hermes with no installed front end is still blocked: %v", plan.Blockers)
	}
}
