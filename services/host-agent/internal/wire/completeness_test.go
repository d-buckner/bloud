// SPDX-License-Identifier: AGPL-3.0-only

package wire

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
)

// optionalConfigFields are the OrchestratorConfig fields that Build is allowed
// to leave zero, each with the reason it may be.
//
// This table is the point of the test. Adding a field to OrchestratorConfig
// makes it uncovered, which fails the build with the field named, until
// whoever added it either wires it here or records why it is legitimately
// optional. Adding an entry to this table is a deliberate, reviewable act,
// which is the whole value: "is this subsystem wired?" stops being a
// question you have to remember to ask.
var optionalConfigFields = map[string]string{
	// Both budgets are deliberately zero: NewOrchestrator substitutes the
	// framework default (DefaultPostStartBudget) when a caller does not
	// override it, so a zero here means "use the default", not "unset".
	"HealthCheckTimeout": "zero means no per-app health timeout; the caller's context deadline applies",
	"PostStartBudget":    "zero means NewOrchestrator applies DefaultPostStartBudget",
}

// TestBuildCoversEveryOrchestratorConfigField is the drift guard.
//
// It reflects over the config type the orchestrator is built with and demands
// that every field is either populated by Build or listed above with a
// reason. A newly added field that nobody wired shows up here by name instead
// of showing up later as a subsystem that silently does nothing.
func TestBuildCoversEveryOrchestratorConfigField(t *testing.T) {
	out, err := Build(baseInput(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	cfg := reflect.ValueOf(out.Config)
	cfgType := cfg.Type()

	var uncovered []string
	var stale []string

	for i := 0; i < cfgType.NumField(); i++ {
		field := cfgType.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Name
		populated := !cfg.Field(i).IsZero()

		if populated {
			if _, listed := optionalConfigFields[name]; listed {
				stale = append(stale, name)
			}
			continue
		}
		if _, listed := optionalConfigFields[name]; !listed {
			uncovered = append(uncovered, name)
		}
	}

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
// fields would pass every assertion above vacuously.
func TestConfigTypeHasFieldsToGuard(t *testing.T) {
	cfgType := reflect.TypeOf(orchestrator.OrchestratorConfig{})
	exported := 0
	for i := 0; i < cfgType.NumField(); i++ {
		if cfgType.Field(i).IsExported() {
			exported++
		}
	}
	if exported < 20 {
		t.Fatalf("expected the orchestrator config to expose at least 20 fields, got %d; the guard would be near-vacuous", exported)
	}
	if len(optionalConfigFields) >= exported/2 {
		t.Fatalf("allowlist (%d) covers half or more of the config (%d); the guard is too permissive to be worth running",
			len(optionalConfigFields), exported)
	}
}
