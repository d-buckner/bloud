// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import "fmt"

// InstallPlan describes what will happen when installing an app
type InstallPlan struct {
	App        string `json:"app"`
	CanInstall bool   `json:"canInstall"`

	// Why can't we install (if CanInstall is false)
	Blockers []string `json:"blockers"`

	// RequiredProviders are the declared default providers of required
	// contracts that are not installed yet. Installing the app installs these
	// with it, one entry per contract.
	RequiredProviders []ConfigTask `json:"requiredProviders"`

	// AutoConfig are the installed providers the app wires to: every installed
	// compatible provider, one entry each.
	AutoConfig []ConfigTask `json:"autoConfig"`

	// Installed apps that will be configured to use this new app
	Dependents []ConfigTask `json:"dependents"`
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
		App:               appName,
		CanInstall:        true,
		Blockers:          []string{},
		RequiredProviders: []ConfigTask{},
		AutoConfig:        []ConfigTask{},
		Dependents:        []ConfigTask{},
	}

	for intName, integration := range app.Integrations {
		installed, _ := g.GetCompatibleApps(appName, intName)

		if len(installed) == 0 {
			if !integration.Required {
				// Nothing installed, not required: nothing to wire, nothing to
				// install. The app simply runs without that provider.
				continue
			}
			// Required and nothing installed: the declared default provider is
			// installed with the app. There is no choice to make.
			if provider := defaultProvider(integration); provider != "" {
				plan.RequiredProviders = append(plan.RequiredProviders, ConfigTask{
					Target:      appName,
					Source:      provider,
					Integration: intName,
				})
			}
			continue
		}

		// The set model: every installed compatible provider is wired. Nothing
		// is picked among them.
		for _, opt := range installed {
			plan.AutoConfig = append(plan.AutoConfig, ConfigTask{
				Target:      appName,
				Source:      opt.App,
				Integration: intName,
			})
		}
	}

	// Find apps that will integrate with this new app
	plan.Dependents = g.FindDependents(appName)

	return plan, nil
}

// defaultProvider returns the provider an integration resolves to by default:
// the entry flagged default. A required integration is validated at load to
// carry exactly one, so the answer is unambiguous. The value is a catalog app
// ID; an instance-source default comes back empty because there is no app to
// install for it.
func defaultProvider(integration Integration) string {
	for _, compat := range integration.Compatible {
		if compat.Default {
			return compat.App
		}
	}
	return ""
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
