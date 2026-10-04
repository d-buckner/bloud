// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

// testInference is a small manifest shaped like the real validation.yaml
// inference block: module globs ordered most-specific first, a wildcard for
// root config files, and a `**` catch-all for the whole-tree checks.
var testInference = validationManifest{
	Inference: manifestInference{
		Paths: []manifestPath{
			{Pattern: "services/host-agent/web/**", Triggers: []string{"web-unit", "web-check-host-agent", "web-lint", "web-build"}},
			{Pattern: "services/host-agent/**", Triggers: []string{"go-host-agent", "go-host-agent-race", "go-lint-host-agent", "gofmt"}},
			{Pattern: "apps/**", Triggers: []string{"go-apps", "go-lint-apps", "gofmt"}, RiskAreas: []string{"app-install"}},
			{Pattern: "cli/**", Triggers: []string{"go-cli", "go-lint-cli", "gofmt"}},
			{Pattern: "e2e/**", Triggers: []string{}, RiskAreas: []string{"playwright"}},
			{Pattern: "package.json", Triggers: []string{"*"}},
			{Pattern: "**", Triggers: []string{"license-headers", "prose-lint", "no-emdash", "docs-links", "file-length"}},
		},
	},
}

func TestInferTriggersMostSpecificWins(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  map[string]bool
	}{
		{
			name:  "web file triggers web checks, not the Go suite",
			files: []string{"services/host-agent/web/src/App.svelte"},
			want:  map[string]bool{"web-unit": true, "web-check-host-agent": true, "web-lint": true, "web-build": true, "license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
		{
			name:  "host-agent go file triggers go checks and gofmt",
			files: []string{"services/host-agent/cmd/host-agent/main.go"},
			want:  map[string]bool{"go-host-agent": true, "go-host-agent-race": true, "go-lint-host-agent": true, "gofmt": true, "license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
		{
			name:  "apps go file triggers apps checks",
			files: []string{"apps/jellyfin/configurator.go"},
			want:  map[string]bool{"go-apps": true, "go-lint-apps": true, "gofmt": true, "license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
		{
			name:  "cli go file triggers cli checks",
			files: []string{"cli/validate.go"},
			want:  map[string]bool{"go-cli": true, "go-lint-cli": true, "gofmt": true, "license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
		{
			name:  "docs file triggers only the catch-all",
			files: []string{"docs/plans/something.md"},
			want:  map[string]bool{"license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
		{
			name:  "e2e file triggers no module commands, only the catch-all",
			files: []string{"e2e/tests/jellyfin.spec.ts"},
			want:  map[string]bool{"license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
		{
			name:  "root config file triggers the wildcard",
			files: []string{"package.json"},
			want:  map[string]bool{"*": true, "license-headers": true, "prose-lint": true, "no-emdash": true, "docs-links": true, "file-length": true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, unmapped := inferTriggers(tc.files, &testInference)
			if len(unmapped) != 0 {
				t.Errorf("unmapped = %v, want none (the catch-all matches every file)", unmapped)
			}
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("trigger %q = %v, want %v (full set: %v)", id, got[id], want, got)
				}
			}
			for id := range got {
				if !tc.want[id] {
					t.Errorf("unexpected trigger %q (full set: %v)", id, got)
				}
			}
		})
	}
}

func TestInferTriggersRiskAreas(t *testing.T) {
	_, riskAreas, _ := inferTriggers([]string{"apps/jellyfin/configurator.go"}, &testInference)
	if len(riskAreas) != 1 || riskAreas[0] != "app-install" {
		t.Errorf("risk areas = %v, want [app-install]", riskAreas)
	}
}

func TestTriggeredCommandsWildcard(t *testing.T) {
	tier := manifestTier{
		Commands: []manifestCommand{
			{ID: "a"},
			{ID: "b"},
			{ID: "c"},
		},
	}

	got := triggeredCommands(tier, map[string]bool{"*": true})
	if len(got) != 3 {
		t.Fatalf("wildcard returned %d commands, want 3", len(got))
	}
	for i, id := range []string{"a", "b", "c"} {
		if got[i].ID != id {
			t.Errorf("wildcard command %d = %q, want %q", i, got[i].ID, id)
		}
	}

	specific := triggeredCommands(tier, map[string]bool{"b": true})
	if len(specific) != 1 || specific[0].ID != "b" {
		t.Fatalf("specific trigger returned %v, want just [b]", specific)
	}
}

// TestRealManifestInference loads the repo's own validation.yaml and pins the
// realManifest loads the repo's validation.yaml, skipping when no project root
// is available (e.g. a vendored test run).
func realManifest(t *testing.T) *validationManifest {
	t.Helper()
	root, err := getProjectRoot()
	if err != nil {
		t.Skipf("no project root available: %v", err)
	}
	manifest, err := loadManifest(root)
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	return manifest
}

// TestRealManifestEveryCommandReachable pins that no fast-tier command is
// orphaned: each must be reachable from some inference path or from the `*`
// wildcard. A command nobody can trigger is a check CI silently never runs.
func TestRealManifestEveryCommandReachable(t *testing.T) {
	manifest := realManifest(t)
	fast, ok := manifest.Tiers["fast"]
	if !ok {
		t.Fatal("no fast tier in validation.yaml")
	}

	reachable := map[string]bool{"*": true}
	for _, p := range manifest.Inference.Paths {
		for _, trig := range p.Triggers {
			reachable[trig] = true
		}
	}
	for _, cmd := range fast.Commands {
		if !reachable[cmd.ID] {
			t.Errorf("fast-tier command %q is not reachable from any inference path", cmd.ID)
		}
	}
}

// TestRealManifestWebChangeSkipsGoSuite pins the most-specific-wins rule: a web
// change must trigger the web checks, not the host-agent Go suite.
func TestRealManifestWebChangeSkipsGoSuite(t *testing.T) {
	manifest := realManifest(t)
	triggers, _, _ := inferTriggers([]string{"services/host-agent/web/src/App.svelte"}, manifest)
	if triggers["go-host-agent"] || triggers["go-host-agent-race"] || triggers["go-lint-host-agent"] {
		t.Errorf("web change pulled in the Go suite: %v", triggers)
	}
	if !triggers["web-build"] {
		t.Errorf("web change did not trigger web-build: %v", triggers)
	}
}

// TestRealManifestDocsChangeOnlyCatchAll pins that a docs change triggers the
// whole-tree catch-all checks and no module command.
func TestRealManifestDocsChangeOnlyCatchAll(t *testing.T) {
	manifest := realManifest(t)
	triggers, _, _ := inferTriggers([]string{"docs/plans/something.md"}, manifest)
	for id := range triggers {
		if id == "go-host-agent" || id == "go-apps" || id == "go-cli" || id == "web-build" || id == "gofmt" {
			t.Errorf("docs change triggered a module command %q: %v", id, triggers)
		}
	}
	if !triggers["prose-lint"] || !triggers["file-length"] {
		t.Errorf("docs change missing a whole-tree check: %v", triggers)
	}
}

// TestRealManifestRootConfigTriggersWildcard pins that a root config file
// triggers the full tier.
func TestRealManifestRootConfigTriggersWildcard(t *testing.T) {
	manifest := realManifest(t)
	triggers, _, _ := inferTriggers([]string{"package.json"}, manifest)
	if !triggers["*"] {
		t.Errorf("package.json change did not trigger the wildcard: %v", triggers)
	}
}
