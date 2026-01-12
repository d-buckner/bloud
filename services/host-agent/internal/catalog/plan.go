package catalog

import "fmt"

// InstallPlan describes what will happen when installing an app
type InstallPlan struct {
	App        string `json:"app"`
	CanInstall bool   `json:"canInstall"`

	// Why can't we install (if CanInstall is false)
	Blockers []string `json:"blockers"`

	// Integrations that need user choice
	Choices []IntegrationChoice `json:"choices"`

	// Auto-resolved integrations (exactly one compatible app installed)
	AutoConfig []ConfigTask `json:"autoConfig"`

	// Installed apps that will be configured to use this new app
	Dependents []ConfigTask `json:"dependents"`
}

// IntegrationChoice presents options when multiple compatible apps exist
type IntegrationChoice struct {
	Integration string           `json:"integration"`
	Required    bool             `json:"required"`
	Installed   []ChoiceOption   `json:"installed"`
	Available   []ChoiceOption   `json:"available"`
	Recommended string           `json:"recommended,omitempty"`
}

// ChoiceOption is a single option in an integration choice
type ChoiceOption struct {
	App      string `json:"app"`
	Default  bool   `json:"default"`
	Category string `json:"category,omitempty"`
}

// RemovePlan describes what will happen when removing an app
type RemovePlan struct {
	App       string   `json:"app"`
	CanRemove bool     `json:"canRemove"`
	Blockers  []string `json:"blockers"`

	// Apps that will have this integration removed
	WillUnconfigure []string `json:"willUnconfigure"`
}

// PlanInstall computes what happens when installing an app
func (g *AppGraph) PlanInstall(appName string) (*InstallPlan, error) {
	app, ok := g.Apps[appName]
	if !ok {
		return nil, fmt.Errorf("unknown app: %s", appName)
	}

	plan := &InstallPlan{
		App:        appName,
		CanInstall: true,
		Choices:    []IntegrationChoice{},
		AutoConfig: []ConfigTask{},
		Dependents: []ConfigTask{},
	}

	for intName, integration := range app.Integrations {
		installed, available := g.GetCompatibleApps(appName, intName)

		switch {
		case len(installed) == 0 && integration.Required:
			// Nothing installed, required - need to choose what to install
			plan.Choices = append(plan.Choices, makeChoice(intName, integration, installed, available))

		case len(installed) == 0 && !integration.Required:
			// Nothing installed, not required - skip
			continue

		case len(installed) == 1:
			// Exactly one installed - auto-configure
			plan.AutoConfig = append(plan.AutoConfig, ConfigTask{
				Target:      appName,
				Source:      installed[0].App,
				Integration: intName,
			})

		case len(installed) > 1 && !integration.Multi:
			// Multiple installed but can only use one - need to choose
			plan.Choices = append(plan.Choices, makeChoice(intName, integration, installed, available))

		case len(installed) > 1 && integration.Multi:
			// Multiple installed and can use all - auto-configure all
			for _, opt := range installed {
				plan.AutoConfig = append(plan.AutoConfig, ConfigTask{
					Target:      appName,
					Source:      opt.App,
					Integration: intName,
				})
			}
		}
	}

	// Find apps that will integrate with this new app
	plan.Dependents = g.FindDependents(appName)

	return plan, nil
}

// PlanRemove computes what happens when removing an app
func (g *AppGraph) PlanRemove(appName string) (*RemovePlan, error) {
	_, ok := g.Apps[appName]
	if !ok {
		return nil, fmt.Errorf("unknown app: %s", appName)
	}

	plan := &RemovePlan{
		App:             appName,
		CanRemove:       true,
		Blockers:        []string{},
		WillUnconfigure: []string{},
	}

	// Find installed apps that depend on this one
	dependents := g.FindDependents(appName)

	for _, dep := range dependents {
		depApp := g.Apps[dep.Target]
		integration := depApp.Integrations[dep.Integration]

		// Are there other installed apps that could fill this slot?
		alternatives := g.findAlternatives(dep.Target, dep.Integration, appName)

		if integration.Required && len(alternatives) == 0 {
			plan.CanRemove = false
			plan.Blockers = append(plan.Blockers,
				fmt.Sprintf("%s requires a %s", dep.Target, dep.Integration))
		} else {
			plan.WillUnconfigure = append(plan.WillUnconfigure, dep.Target)
		}
	}

	return plan, nil
}

// findAlternatives finds other installed apps that can fill an integration slot
func (g *AppGraph) findAlternatives(appName, integrationName, excluding string) []string {
	app := g.Apps[appName]
	integration := app.Integrations[integrationName]

	var alternatives []string
	for _, compat := range integration.Compatible {
		if compat.App == excluding {
			continue
		}
		if g.installedSet[compat.App] {
			alternatives = append(alternatives, compat.App)
		}
	}

	return alternatives
}

func makeChoice(intName string, integration Integration, installed, available []CompatibleApp) IntegrationChoice {
	choice := IntegrationChoice{
		Integration: intName,
		Required:    integration.Required,
	}

	for _, c := range installed {
		choice.Installed = append(choice.Installed, ChoiceOption{
			App:      c.App,
			Default:  c.Default,
			Category: c.Category,
		})
		if c.Default {
			choice.Recommended = c.App
		}
	}

	for _, c := range available {
		choice.Available = append(choice.Available, ChoiceOption{
			App:      c.App,
			Default:  c.Default,
			Category: c.Category,
		})
		if c.Default && choice.Recommended == "" {
			choice.Recommended = c.App
		}
	}

	return choice
}
