// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Manifest types for validation.yaml
type validationManifest struct {
	Tiers     map[string]manifestTier `yaml:"tiers"`
	Inference manifestInference       `yaml:"inference"`
	Apps      map[string]manifestApp  `yaml:"apps"`
}

type manifestTier struct {
	Commands []manifestCommand `yaml:"commands"`
}

type manifestCommand struct {
	ID  string `yaml:"id"`
	Cwd string `yaml:"cwd"`
	Run string `yaml:"run"`
	// Heavy marks a command that parallelises internally across every core
	// (go test, golangci-lint) and must therefore not share the machine with
	// another heavy command. The fast tier runs heavy commands one at a time
	// and runs light commands concurrently alongside them; see runCommands.
	Heavy bool `yaml:"heavy"`
}

type manifestInference struct {
	Paths []manifestPath `yaml:"paths"`
}

type manifestPath struct {
	Pattern   string   `yaml:"pattern"`
	Triggers  []string `yaml:"triggers"`
	RiskAreas []string `yaml:"riskAreas"`
}

type manifestApp struct {
	Auth            string   `yaml:"auth"`
	ValidationLevel string   `yaml:"validation-level"`
	Files           []string `yaml:"files"`
	E2EProject      string   `yaml:"e2e-project"`
}

func loadManifest(root string) (*validationManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "validation.yaml"))
	if err != nil {
		return nil, err
	}
	var m validationManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// --- Fast tier ---

// inferTriggers maps changed files through the manifest's inference globs:
// which validation command IDs are triggered and which risk areas are hit.
//
// The `**` catch-all matches every file and its checks are whole-tree on
// purpose (doc links, prose, image pins, file length), so its triggers are
// applied once for every run. The remaining patterns are module globs ordered
// most-specific first: the first one to match a file claims it, so
// services/host-agent/web/** claims web files before services/host-agent/**
// would sweep them into the Go suite.
func inferTriggers(changedFiles []string, manifest *validationManifest) (map[string]bool, []string, []string) {
	triggeredIDs := map[string]bool{}
	riskAreaSet := map[string]bool{}

	for _, p := range manifest.Inference.Paths {
		if p.Pattern == "**" {
			addTriggers(p, triggeredIDs, riskAreaSet)
			break
		}
	}

	for _, f := range changedFiles {
		for _, p := range manifest.Inference.Paths {
			if p.Pattern == "**" {
				continue
			}
			if pathMatches(f, p.Pattern) {
				addTriggers(p, triggeredIDs, riskAreaSet)
				break
			}
		}
	}

	var riskAreas []string
	for r := range riskAreaSet {
		riskAreas = append(riskAreas, r)
	}
	sort.Strings(riskAreas)
	// No file is ever unmapped: the `**` catch-all matches everything, so the
	// confidence stays "high". The slice is kept for the ledger's shape.
	return triggeredIDs, riskAreas, nil
}

// addTriggers merges a pattern's trigger IDs and risk areas into the running
// sets.
func addTriggers(p manifestPath, triggeredIDs map[string]bool, riskAreaSet map[string]bool) {
	for _, t := range p.Triggers {
		triggeredIDs[t] = true
	}
	for _, r := range p.RiskAreas {
		riskAreaSet[r] = true
	}
}

// detectAffectedApps returns the sorted names of catalog apps whose file
// globs match any changed file.
func detectAffectedApps(changedFiles []string, manifest *validationManifest) []string {
	appSet := map[string]bool{}
	for appName, appDef := range manifest.Apps {
		for _, f := range changedFiles {
			if appSet[appName] {
				break
			}
			for _, pattern := range appDef.Files {
				if pathMatches(f, pattern) {
					appSet[appName] = true
					break
				}
			}
		}
	}
	var apps []string
	for a := range appSet {
		apps = append(apps, a)
	}
	sort.Strings(apps)
	return apps
}

func getChangedFiles(root string, since string) ([]string, error) {
	// Get both staged and unstaged changes
	var args []string
	if since != "" {
		args = []string{"diff", "--name-only", since}
	} else {
		args = []string{"diff", "--name-only", "HEAD"}
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// HEAD might not exist (initial commit), fall back to listing all tracked + untracked
		cmd2 := exec.Command("git", "status", "--porcelain")
		cmd2.Dir = root
		out2, err2 := cmd2.Output()
		if err2 != nil {
			return nil, err2
		}
		return parseStatusFiles(string(out2)), nil
	}

	files := splitLines(string(out))

	// Also get staged changes not yet committed
	cmd2 := exec.Command("git", "diff", "--name-only", "--cached")
	cmd2.Dir = root
	out2, err := cmd2.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached failed: %w", err)
	}
	staged := splitLines(string(out2))

	// Also get untracked files
	cmd3 := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd3.Dir = root
	out3, err := cmd3.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others failed: %w", err)
	}
	untracked := splitLines(string(out3))

	// Deduplicate
	seen := map[string]bool{}
	var result []string
	for _, list := range [][]string{files, staged, untracked} {
		for _, f := range list {
			if f != "" && !seen[f] {
				seen[f] = true
				result = append(result, f)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func parseStatusFiles(status string) []string {
	var files []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		f := strings.TrimSpace(line[3:])
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// pathMatches checks if a file path matches a glob-like pattern.
// Supports ** for recursive directory matching and * for single segment.
func pathMatches(file, pattern string) bool {
	// Convert glob pattern to a simple prefix + suffix check
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return strings.HasPrefix(file, prefix+"/") || file == prefix
	}
	if strings.Contains(pattern, "**") {
		// pattern like "a/**/b": split and check prefix/suffix
		parts := strings.SplitN(pattern, "**", 2)
		return strings.HasPrefix(file, parts[0]) && strings.HasSuffix(file, strings.TrimPrefix(parts[1], "/"))
	}
	// Exact match or single-level glob
	matched, _ := filepath.Match(pattern, file)
	return matched
}
