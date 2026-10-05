// SPDX-License-Identifier: AGPL-3.0-only

package wire

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
)

// optionalConfigFields are the OrchestratorConfig fields (across the subsystem
// structs) that Build is allowed to leave zero, each with the reason it may be.
//
// This table is the point of the test. Adding a field to any subsystem struct
// makes it uncovered, which fails the build with the field named, until
// whoever added it either wires it here or records why it is legitimately
// optional. Adding an entry to this table is a deliberate, reviewable act,
// which is the whole value: "is this subsystem wired?" stops being a
// question you have to remember to ask.
var optionalConfigFields = map[string]string{
	// Both budgets are deliberately zero: NewOrchestrator substitutes the
	// framework default (DefaultAppPhaseBudget) when a caller does not
	// override it, so a zero here means "use the default", not "unset".
	"HealthCheckTimeout": "zero means no per-app health timeout; the caller's context deadline applies",
	"AppPhaseBudget":     "zero means NewOrchestrator applies DefaultAppPhaseBudget",
	// The resync warning threshold is not a per-deployment knob today: the
	// product value is DefaultResyncRestartWarnAt and the watchdog applies it
	// when this stays zero. It is a field rather than a constant so a future
	// operator override has somewhere to land without a new seam.
	"ResyncRestartWarnAt": "zero means the watchdog applies DefaultResyncRestartWarnAt",
}

// forEachExportedField walks every exported field of OrchestratorConfig,
// recursing into the subsystem structs, and calls fn with the field name and
// its reflect.Value. It is how the drift guard sees the full config surface,
// not just the grouped top level.
func forEachExportedField(v reflect.Value, t reflect.Type, fn func(name string, fv reflect.Value)) {
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		if !ft.IsExported() {
			continue
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			forEachExportedField(fv, fv.Type(), fn)
			continue
		}
		fn(ft.Name, fv)
	}
}

// TestBuildCoversEveryOrchestratorConfigField is the drift guard.
//
// It reflects over the config type the orchestrator is built with and demands
// that every field, including those inside the subsystem structs, is either
// populated by Build or listed above with a reason. A newly added field that
// nobody wired shows up here by name instead of showing up later as a
// subsystem that silently does nothing.
func TestBuildCoversEveryOrchestratorConfigField(t *testing.T) {
	out, err := Build(baseInput(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	cfg := reflect.ValueOf(out.Config)

	var uncovered []string
	var stale []string

	forEachExportedField(cfg, cfg.Type(), func(name string, fv reflect.Value) {
		populated := !fv.IsZero()

		if populated {
			if _, listed := optionalConfigFields[name]; listed {
				stale = append(stale, name)
			}
			return
		}
		if _, listed := optionalConfigFields[name]; !listed {
			uncovered = append(uncovered, name)
		}
	})

	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("OrchestratorConfig fields Build never sets: %s\n"+
			"Either wire them in Build, or add each to optionalConfigFields with the reason it may stay zero.",
			strings.Join(uncovered, ", "))
	}

	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("optionalConfigFields lists fields Build actually populates: %s\n"+
			"Remove them from the table: an allowlist entry that guards nothing is how an allowlist rots.",
			strings.Join(stale, ", "))
	}
}

// The guard must actually see the field count. A reflection bug that walked no
// fields would pass every assertion above vacuously. The count is over the
// subsystem structs, not the grouped top level, so it stays meaningful as the
// grouping changes.
func TestConfigTypeHasFieldsToGuard(t *testing.T) {
	cfg := reflect.ValueOf(orchestrator.OrchestratorConfig{})
	exported := 0
	forEachExportedField(cfg, cfg.Type(), func(_ string, _ reflect.Value) {
		exported++
	})
	if exported < 20 {
		t.Fatalf("expected the orchestrator config to expose at least 20 fields, got %d; the guard would be near-vacuous", exported)
	}
	if len(optionalConfigFields) >= exported/2 {
		t.Fatalf("allowlist (%d) covers half or more of the config (%d); the guard is too permissive to be worth running",
			len(optionalConfigFields), exported)
	}
}
