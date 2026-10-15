// SPDX-License-Identifier: AGPL-3.0-only

package catalog

// AppGraph manages app relationships and dependency resolution
type AppGraph struct {
	Apps      map[string]*App `json:"apps"`
	Installed []string        `json:"installed"`

	// Reverse index: app -> who integrates with it
	dependents   map[string][]IntegrationRef
	installedSet map[string]bool

	// configuredContracts is the set of contracts an operator-filled Settings row
	// satisfies today, for example `transcription` or `embeddings`. A
	// `source: setting` provider has no container and no install row, so the graph
	// cannot see it the way it sees an installed app, and without this set an app
	// that genuinely requires one would plan as installable and then fail at its
	// first startup.
	configuredContracts map[string]bool
}

// NewGraph creates a new AppGraph from a slice of app definitions
func NewGraph(apps []*App) *AppGraph {
	g := &AppGraph{
		Apps:                make(map[string]*App),
		Installed:           []string{},
		dependents:          make(map[string][]IntegrationRef),
		installedSet:        make(map[string]bool),
		configuredContracts: make(map[string]bool),
	}

	for _, app := range apps {
		g.Apps[app.CatalogID] = app
		g.buildDependents(app)
	}

	return g
}

// buildDependents populates the reverse index for an app
func (g *AppGraph) buildDependents(app *App) {
	for intName, integration := range app.Integrations {
		for _, compat := range integration.Compatible {
			g.dependents[compat.App] = append(g.dependents[compat.App], IntegrationRef{
				App:         app.CatalogID,
				Integration: intName,
			})
		}
	}
}

func (g *AppGraph) SetInstalled(installed []string) {
	g.Installed = installed
	g.installedSet = make(map[string]bool)
	for _, name := range installed {
		g.installedSet[name] = true
	}
}

// SetConfiguredContracts updates which contracts a Settings row satisfies. It is
// the settings analogue of SetInstalled and is refreshed on the same pass, so a
// plan never reads a stale view of what the operator filled in.
func (g *AppGraph) SetConfiguredContracts(contracts []string) {
	g.configuredContracts = make(map[string]bool, len(contracts))
	for _, name := range contracts {
		g.configuredContracts[name] = true
	}
}

// IsConfiguredContract reports whether a Settings row fills this contract today.
func (g *AppGraph) IsConfiguredContract(contract string) bool {
	return g.configuredContracts[contract]
}

// IsInstalled checks if an app is installed
func (g *AppGraph) IsInstalled(appName string) bool {
	return g.installedSet[appName]
}

// FindDependents returns installed apps that integrate with the given app
func (g *AppGraph) FindDependents(appName string) []ConfigTask {
	var tasks []ConfigTask

	for _, ref := range g.dependents[appName] {
		if !g.installedSet[ref.App] {
			continue
		}

		tasks = append(tasks, ConfigTask{
			Target:      ref.App,
			Source:      appName,
			Integration: ref.Integration,
		})
	}

	return tasks
}

// GetCompatibleApps returns compatible apps for an integration, split by installed status
func (g *AppGraph) GetCompatibleApps(appName string, integrationName string) (installed []CompatibleApp, available []CompatibleApp) {
	app, ok := g.Apps[appName]
	if !ok {
		return nil, nil
	}

	integration, ok := app.Integrations[integrationName]
	if !ok {
		return nil, nil
	}

	for _, compat := range integration.Compatible {
		if g.installedSet[compat.App] {
			installed = append(installed, compat)
		} else {
			available = append(available, compat)
		}
	}

	return installed, available
}

func (g *AppGraph) GetApps() map[string]*App {
	return g.Apps
}
