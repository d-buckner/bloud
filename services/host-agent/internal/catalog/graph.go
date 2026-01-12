package catalog

// AppGraph manages app relationships and dependency resolution
type AppGraph struct {
	Apps      map[string]*AppDefinition `json:"apps"`
	Installed []string                  `json:"installed"`

	// Reverse index: app -> who integrates with it
	dependents  map[string][]IntegrationRef
	installedSet map[string]bool
}

// NewGraph creates a new AppGraph from a slice of app definitions
func NewGraph(apps []*AppDefinition) *AppGraph {
	g := &AppGraph{
		Apps:         make(map[string]*AppDefinition),
		Installed:    []string{},
		dependents:   make(map[string][]IntegrationRef),
		installedSet: make(map[string]bool),
	}

	for _, app := range apps {
		g.Apps[app.Name] = app
		g.buildDependents(app)
	}

	return g
}

// buildDependents populates the reverse index for an app
func (g *AppGraph) buildDependents(app *AppDefinition) {
	for intName, integration := range app.Integrations {
		for _, compat := range integration.Compatible {
			g.dependents[compat.App] = append(g.dependents[compat.App], IntegrationRef{
				App:         app.Name,
				Integration: intName,
			})
		}
	}
}

// SetInstalled updates which apps are installed
func (g *AppGraph) SetInstalled(installed []string) {
	g.Installed = installed
	g.installedSet = make(map[string]bool)
	for _, name := range installed {
		g.installedSet[name] = true
	}
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

// GetApps returns all app definitions
func (g *AppGraph) GetApps() map[string]*AppDefinition {
	return g.Apps
}
