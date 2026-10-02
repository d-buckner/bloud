// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

// A pull request's change set is every commit since the branch diverged, not
// the latest push. The old `--since <previous tip>` diff saw only the last
// commit, so an app changed earlier in the PR was skipped and its e2e leg did
// not run at all.
func TestMergeBaseWithCoversEveryCommitInThePR(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-b", "main")
	write("README.md", "base\n")
	git("add", ".")
	git("commit", "-m", "base")

	git("checkout", "-b", "feature")
	write("apps/jellyfin/metadata.yaml", "name: jellyfin\n")
	git("add", ".")
	git("commit", "-m", "touch jellyfin")

	// The latest push is documentation only. The PR still changed Jellyfin.
	write("docs.md", "docs\n")
	git("add", ".")
	git("commit", "-m", "docs only")

	mergeBase, err := mergeBaseWith(dir, "main")
	if err != nil {
		t.Fatalf("mergeBaseWith: %v", err)
	}
	files, err := changedFilesSince(dir, mergeBase)
	if err != nil {
		t.Fatalf("changedFilesSince: %v", err)
	}
	if !strings.Contains(strings.Join(files, ","), "apps/jellyfin/metadata.yaml") {
		t.Fatalf("the PR diff %v dropped a change from an earlier commit", files)
	}
}
