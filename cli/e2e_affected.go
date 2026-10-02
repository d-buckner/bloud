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

// e2eAffectedFlags is the parsed command line of `bloud e2e affected`.
type e2eAffectedFlags struct {
	since  string
	base   string
	asJSON bool
}

// parseE2EAffectedFlags parses the flags. `--since` is an exact diff base and
// `--base` is a branch to diff from the merge base with; they are mutually
// exclusive because the two mean different ranges.
func parseE2EAffectedFlags(args []string) (e2eAffectedFlags, error) {
	var f e2eAffectedFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 < len(args) {
				i++
				f.since = args[i]
			}
		case "--base":
			if i+1 < len(args) {
				i++
				f.base = args[i]
			}
		case "--json":
			f.asJSON = true
		default:
			return f, fmt.Errorf("unknown flag %q (usage: bloud e2e affected [--since <ref> | --base <ref>] [--json])", args[i])
		}
	}
	if f.since != "" && f.base != "" {
		return f, fmt.Errorf("--since and --base are mutually exclusive")
	}
	return f, nil
}

// runE2EAffected prints the e2e projects the current change set needs. CI uses
// it to shrink the e2e matrix: an app-only change runs only that app, anything
// outside the app directories runs every app, and a markdown-only change runs
// nothing.
func runE2EAffected(root string, args []string) error {
	flags, err := parseE2EAffectedFlags(args)
	if err != nil {
		return err
	}

	manifest, err := loadManifest(root)
	if err != nil {
		return err
	}

	changed, err := affectedFiles(root, flags)
	if err != nil {
		return err
	}

	apps := affectedE2EProjects(changed, manifest)

	if flags.asJSON {
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

// affectedFiles resolves the change set the flags describe. An unresolvable
// base is a warning, not a failure: a nil slice widens to every app, which is
// the safe direction.
func affectedFiles(root string, flags e2eAffectedFlags) ([]string, error) {
	switch {
	case flags.base != "":
		// A pull request: the change set is every commit since the branch
		// diverged from base, not just the latest push. Diffing from the
		// previous branch tip (the old behavior) narrowed the matrix to what
		// the last push touched, so an app changed earlier in the PR was
		// skipped.
		mergeBase, err := mergeBaseWith(root, flags.base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: git merge-base %s HEAD failed (%v); running every app\n", flags.base, err)
			return nil, nil
		}
		return changedFilesWarn(root, mergeBase)
	case flags.since != "":
		// An explicit base, used for a push to the default branch.
		return changedFilesWarn(root, flags.since)
	default:
		return getChangedFiles(root, "")
	}
}

// changedFilesWarn is changedFilesSince with the CI contract that an
// unresolvable ref (force-push, shallow clone) widens rather than fails.
func changedFilesWarn(root, ref string) ([]string, error) {
	files, err := changedFilesSince(root, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: git diff %s HEAD failed (%v); running every app\n", ref, err)
		return nil, nil
	}
	return files, nil
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

// mergeBaseWith is the commit where HEAD diverged from ref: the change set a
// pull request introduces, however many pushes it took. A commit range query
// from the previous push tip instead sees only the latest push.
func mergeBaseWith(root, ref string) (string, error) {
	cmd := exec.Command("git", "-C", root, "merge-base", ref, "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
