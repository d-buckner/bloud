package catalog

import (
	"testing"
)

func TestNewGraph_BuildsDependents(t *testing.T) {
	apps := []*AppDefinition{
		{
			Name: "qbittorrent",
		},
		{
			Name: "radarr",
			Integrations: map[string]Integration{
				"downloadClient": {
					Required: true,
					Compatible: []CompatibleApp{
						{App: "qbittorrent", Default: true},
					},
				},
			},
		},
	}

	g := NewGraph(apps)

	// Mark radarr as installed so FindDependents returns it
	g.SetInstalled([]string{"radarr"})

	// qbittorrent should have radarr as a dependent via public API
	deps := g.FindDependents("qbittorrent")
	if len(deps) != 1 {
		t.Fatalf("expected 1 dependent, got %d", len(deps))
	}
	if deps[0].Target != "radarr" {
		t.Errorf("expected radarr, got %s", deps[0].Target)
	}
	if deps[0].Integration != "downloadClient" {
		t.Errorf("expected downloadClient, got %s", deps[0].Integration)
	}
}

func TestFindDependents_OnlyReturnsInstalled(t *testing.T) {
	apps := []*AppDefinition{
		{Name: "qbittorrent"},
		{Name: "jellyfin"},
		{
			Name: "radarr",
			Integrations: map[string]Integration{
				"downloadClient": {
					Compatible: []CompatibleApp{{App: "qbittorrent"}},
				},
			},
		},
		{
			Name: "jellyseerr",
			Integrations: map[string]Integration{
				"mediaServer": {
					Compatible: []CompatibleApp{{App: "jellyfin"}},
				},
			},
		},
	}

	g := NewGraph(apps)
	g.SetInstalled([]string{"radarr"}) // only radarr installed, not jellyseerr

	// qbittorrent should only show radarr as dependent (jellyseerr not installed)
	tasks := g.FindDependents("qbittorrent")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Target != "radarr" {
		t.Errorf("expected radarr, got %s", tasks[0].Target)
	}

	// jellyfin should have no dependents (jellyseerr not installed)
	tasks = g.FindDependents("jellyfin")
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestGetCompatibleApps_SplitsByInstalled(t *testing.T) {
	apps := []*AppDefinition{
		{Name: "qbittorrent"},
		{Name: "deluge"},
		{
			Name: "radarr",
			Integrations: map[string]Integration{
				"downloadClient": {
					Compatible: []CompatibleApp{
						{App: "qbittorrent", Default: true},
						{App: "deluge"},
					},
				},
			},
		},
	}

	g := NewGraph(apps)
	g.SetInstalled([]string{"qbittorrent"}) // only qbittorrent installed

	installed, available := g.GetCompatibleApps("radarr", "downloadClient")

	if len(installed) != 1 || installed[0].App != "qbittorrent" {
		t.Errorf("expected qbittorrent installed, got %v", installed)
	}
	if len(available) != 1 || available[0].App != "deluge" {
		t.Errorf("expected deluge available, got %v", available)
	}
}
