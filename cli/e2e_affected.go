// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// runE2EAffected prints the e2e projects the current change set needs. CI uses
// it to shrink the e2e matrix: an app-only change runs only that app, anything
// outside the app directories runs every app, and a markdown-only change runs
// nothing.
func runE2EAffected(root string, args []string) error {
	since := ""
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 < len(args) {
				i++
				since = args[i]
			}
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("unknown flag %q (usage: bloud e2e affected [--since <ref>] [--json])", args[i])
		}
	}

	manifest, err := loadManifest(root)
	if err != nil {
		return err
	}

	var changed []string
	if since != "" {
		// CI passes the push's base commit. A missing base (force-push, shallow
		// clone) must not fail the run: an empty set widens to every app.
		if files, err := changedFilesSince(root, since); err != nil {
			fmt.Fprintf(os.Stderr, "warning: git diff %s HEAD failed (%v); running every app\n", since, err)
		} else {
			changed = files
		}
	} else {
		if changed, err = getChangedFiles(root, ""); err != nil {
			return err
		}
	}

	apps := affectedE2EProjects(changed, manifest)

	if asJSON {
		out, err := json.Marshal(apps)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	for _, app := range apps {
		fmt.Println(app)
	}
	return nil
}

// changedFilesSince lists the files that differ between ref and HEAD. Unlike
// getChangedFiles it ignores the working tree and untracked files: a commit
// range query must not pick up local scratch files.
func changedFilesSince(root, ref string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "diff", "--name-only", ref, "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return splitLines(string(out)), nil
}

// affectedE2EProjects narrows the e2e projects to run for a change set.
//
//   - Markdown does not affect runtime behavior, so a markdown-only change
//     runs nothing (an empty list).
//   - A change confined to catalog app directories runs only those apps'
//     projects.
//   - A file outside them (the framework, shared system apps) or an app
//     without an e2e project runs every project.
//   - An empty change set runs every project: with no diff there is nothing to
//     narrow on, and skipping would be the unsafe default.
func affectedE2EProjects(changedFiles []string, manifest *validationManifest) []string {
	all := e2eProjects(manifest)
	if len(changedFiles) == 0 {
		return all
	}
	code := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		if !isDocumentation(f) {
			code = append(code, f)
		}
	}
	if len(code) == 0 {
		return []string{}
	}
	matched := map[string]bool{}
	for _, f := range code {
		app, ok := appForFile(f, manifest)
		if !ok || app.E2EProject == "" {
			return all
		}
		matched[app.E2EProject] = true
	}
	out := make([]string, 0, len(matched))
	for project := range matched {
		out = append(out, project)
	}
	sort.Strings(out)
	return out
}

// isDocumentation reports whether a path is prose that cannot change runtime
// behavior, so the e2e matrix can ignore it.
func isDocumentation(path string) bool {
	return strings.HasSuffix(path, ".md")
}

// e2eProjects returns every e2e project declared in validation.yaml.
func e2eProjects(manifest *validationManifest) []string {
	seen := map[string]bool{}
	for _, def := range manifest.Apps {
		if def.E2EProject != "" {
			seen[def.E2EProject] = true
		}
	}
	out := make([]string, 0, len(seen))
	for project := range seen {
		out = append(out, project)
	}
	sort.Strings(out)
	return out
}

// appForFile returns the catalog app whose file globs match path.
func appForFile(path string, manifest *validationManifest) (manifestApp, bool) {
	for _, def := range manifest.Apps {
		for _, pattern := range def.Files {
			if pathMatches(path, pattern) {
				return def, true
			}
		}
	}
	return manifestApp{}, false
}
