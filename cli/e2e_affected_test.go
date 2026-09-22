// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
)

func TestAffectedE2EProjects(t *testing.T) {
	manifest := &validationManifest{
		Apps: map[string]manifestApp{
			"jellyfin":  {Files: []string{"apps/jellyfin/**"}, E2EProject: "jellyfin"},
			"navidrome": {Files: []string{"apps/navidrome/**"}, E2EProject: "navidrome"},
		},
	}
	all := []string{"jellyfin", "navidrome"}

	cases := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"one app", []string{"apps/jellyfin/configurator.go"}, []string{"jellyfin"}},
		{"two apps", []string{"apps/jellyfin/configurator.go", "apps/navidrome/metadata.yaml"}, []string{"jellyfin", "navidrome"}},
		{"app plus markdown", []string{"apps/jellyfin/configurator.go", "README.md"}, []string{"jellyfin"}},
		{"framework file", []string{"apps/jellyfin/configurator.go", "services/host-agent/cmd/host-agent/main.go"}, all},
		{"shared system app", []string{"apps/authentik/metadata.yaml"}, all},
		{"top-level apps file", []string{"apps/registry.go"}, all},
		{"markdown only", []string{"README.md", "docs/specs/spec.md"}, []string{}},
		{"markdown in app dir", []string{"apps/jellyfin/INTEGRATION.md"}, []string{}},
		{"empty change set", nil, all},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := affectedE2EProjects(tc.changed, manifest)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// An app without an e2e project cannot be narrowed, so the whole set runs.
func TestAffectedE2EProjectsWidensWithoutE2EProject(t *testing.T) {
	manifest := &validationManifest{
		Apps: map[string]manifestApp{
			"jellyfin": {Files: []string{"apps/jellyfin/**"}, E2EProject: "jellyfin"},
			"newapp":   {Files: []string{"apps/newapp/**"}},
		},
	}
	got := affectedE2EProjects([]string{"apps/newapp/metadata.yaml"}, manifest)
	if strings.Join(got, ",") != "jellyfin" {
		t.Fatalf("got %v, want [jellyfin]", got)
	}
}
