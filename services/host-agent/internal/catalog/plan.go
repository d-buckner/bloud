// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"slices"
)

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
				continue
			}
			// No default app to install means the provider is a Settings row. There
			// is nothing to install alongside the app, so the only question is
			// whether the operator has filled the row.
			if blockers := settingRequirementBlockers(intName, integration, g.IsConfiguredContract); len(blockers) > 0 {
				plan.CanInstall = false
				plan.Blockers = append(plan.Blockers, blockers...)
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

// SettingRequirementBlockers reports why an app cannot be installed yet because
// a contract it requires is filled by a Settings row that is still empty. It is
// exported so the catalog read that feeds the dashboard and the planner that
// gates the install answer with one rule rather than two that can drift.
//
// `isConfigured` is supplied rather than read so the caller decides where truth
// comes from: the graph's refreshed set for a plan, the external registry for a
// catalog read.
func SettingRequirementBlockers(app *App, isConfigured func(contract string) bool) []string {
	if app == nil || isConfigured == nil {
		return nil
	}
	var out []string
	for name, integration := range app.Integrations {
		if !integration.Required {
			continue
		}
		// A required integration with a default app provider installs that provider
		// along with itself, so an empty Settings row is not what stops it.
		if defaultProvider(integration) != "" {
			continue
		}
		out = append(out, settingRequirementBlockers(name, integration, isConfigured)...)
	}
	slices.Sort(out)
	return out
}

// settingRequirementBlockers is the one-arm form the planner calls, where the
// integration has already been found required and unresolvable by an app.
func settingRequirementBlockers(
	name string,
	integration Integration,
	isConfigured func(contract string) bool,
) []string {
	if !declaresSettingProvider(integration) || isConfigured(name) {
		return nil
	}
	return []string{fmt.Sprintf("requires a %s endpoint; configure one under Settings", name)}
}

// declaresSettingProvider reports whether an integration names the instance as a
// provider. A `source: setting` entry has no catalog app behind it, so
// defaultProvider comes back empty for it and the plan needs a different answer
// about what would satisfy the requirement.
func declaresSettingProvider(integration Integration) bool {
	for _, compat := range integration.Compatible {
		if compat.Source == SettingProviderSource {
			return true
		}
	}
	return false
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
