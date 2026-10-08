// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// applyAddExternalAppIntent persists a new external app. PR 1 creates only
// launchers, so the record's kind, source, and values are fixed here rather
// than carried on the intent; later kinds extend the intent instead of this
// path branching on request fields.
func (o *Orchestrator) applyAddExternalAppIntent(intent AddExternalAppIntent) {
	if o.externalApps == nil {
		return
	}
	app := &store.ExternalApp{
		ID:     intent.ID,
		Kind:   string(store.ExternalAppKindLauncher),
		Source: "",
		Name:   intent.Name,
		URL:    intent.URL,
		Icon:   intent.Icon,
		Values: "{}",
	}
	if err := o.externalApps.Upsert(app); err != nil {
		o.logger.Error("failed to add external app", "id", intent.ID, "error", err)
	}
}

// applyUpdateExternalAppIntent changes a launcher's name, URL, or icon. It
// reads the stored row first so an update never resurrects a row that was
// removed between submit and drain.
func (o *Orchestrator) applyUpdateExternalAppIntent(intent UpdateExternalAppIntent) {
	if o.externalApps == nil {
		return
	}
	existing, err := o.externalApps.Get(intent.ID)
	if err != nil || existing == nil {
		o.logger.Warn("cannot update external app", "id", intent.ID, "error", err)
		return
	}
	existing.Name = intent.Name
	existing.URL = intent.URL
	existing.Icon = intent.Icon
	if err := o.externalApps.Upsert(existing); err != nil {
		o.logger.Error("failed to update external app", "id", intent.ID, "error", err)
	}
}

// applyRemoveExternalAppIntent deletes an external app.
func (o *Orchestrator) applyRemoveExternalAppIntent(intent RemoveExternalAppIntent) {
	if o.externalApps == nil {
		return
	}
	if err := o.externalApps.Delete(intent.ID); err != nil {
		o.logger.Error("failed to remove external app", "id", intent.ID, "error", err)
	}
}
