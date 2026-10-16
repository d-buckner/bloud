// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// This file is one question, asked two ways: can this app be installed right now?
//
// A requirement served by a Settings row has no container to install with the
// app, so the only thing that can satisfy it is the operator. The catalog read
// answers the question for every app so a tile can explain itself, and the
// install endpoint answers it for one app before accepting work it would drop.
// Both call `catalog.SettingRequirementBlockers`, so the two answers cannot
// drift into a tile that promises what the endpoint refuses.

// SetExternalAppStore wires the registry that answers which contracts a Settings
// row fills. See the field for why absent means "unknown", not "unmet".
func (m *appsModule) SetExternalAppStore(s store.ExternalAppStoreInterface) {
	m.externalApps = s
}

// configuredContracts returns the set of contracts a Settings row fills right
// now, and whether the lookup worked at all.
func (m *appsModule) configuredContracts() (map[string]bool, bool) {
	if m.externalApps == nil {
		return nil, false
	}
	records, err := m.externalApps.GetAll()
	if err != nil {
		m.logger.Warn("cannot read the external provider registry; reporting no install blockers", "error", err)
		return nil, false
	}
	out := map[string]bool{}
	for _, rec := range records {
		kind, ref, ok := store.ParseExternalAppSource(rec.Source)
		if !ok || kind != store.ExternalAppSourceKindContract {
			continue
		}
		if strings.TrimSpace(rec.URL) != "" {
			out[ref] = true
		}
	}
	return out, true
}

// isConfiguredContract adapts the registry read to the planner's predicate, and
// treats an unavailable lookup as "everything is configured": a surface that
// cannot see the registry must not refuse an install it cannot disprove.
func (m *appsModule) isConfiguredContract(contract string) bool {
	configured, ok := m.configuredContracts()
	if !ok {
		return true
	}
	return configured[contract]
}

// installBlockers names, per catalog app, why it cannot be installed yet. Only
// apps that actually carry a setting-served requirement are considered, so the
// common catalog read does no work: today that is one app in the catalog.
func (m *appsModule) installBlockers(apps []*catalog.App) map[string][]string {
	configured, ok := m.configuredContracts()
	if !ok {
		return nil
	}
	var out map[string][]string
	for _, app := range apps {
		blockers := catalog.SettingRequirementBlockers(app, func(contract string) bool {
			return configured[contract]
		})
		if len(blockers) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[app.CatalogID] = blockers
	}
	return out
}

// installBlockedError is an install the catalog can name but the instance cannot
// yet serve. The handler answers 409 with the blockers verbatim, because the
// sentence that explains the requirement to an operator is the same sentence the
// planner produced for a log line.
type installBlockedError struct {
	app      string
	blockers []string
}

func (e *installBlockedError) Error() string { return strings.Join(e.blockers, "; ") }

// Blockers returns the reasons, for the response body.
func (e *installBlockedError) Blockers() []string { return e.blockers }
