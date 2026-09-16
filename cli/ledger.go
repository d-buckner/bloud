// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ValidateResult is the final ledger written after a validation run.
type ValidateResult struct {
	StartedAt        string           `json:"startedAt"`
	FinishedAt       string           `json:"finishedAt"`
	Tier             string           `json:"tier"`
	ExitCode         int              `json:"exitCode"`
	Apps             []string         `json:"apps"`
	ChangedFiles     []string         `json:"changedFiles"`
	RiskAreas        []string         `json:"riskAreas"`
	Confidence       string           `json:"confidence"`
	ConfidenceReason string           `json:"confidenceReason"`
	Commands         []CommandResult  `json:"commands"`
	Skipped          []SkippedCommand `json:"skipped"`
	UnmappedFiles    []string         `json:"unmappedFiles"`
	Artifacts        []string         `json:"artifacts"`
	NextRecommended  string           `json:"nextRecommendedTier"`
}

// CommandResult records the outcome of a single validation command.

// CommandResult records the outcome of a single validation command.
type CommandResult struct {
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	ExitCode   int    `json:"exitCode"`
}

// SkippedCommand records a command that was not run and why.

// SkippedCommand records a command that was not run and why.
type SkippedCommand struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Manifest types for validation.yaml

func writeLedger(root string, result *ValidateResult, flags validateFlags) {
	if flags.json {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	}

	// Write to .bloud/validation/
	dir := filepath.Join(root, ".bloud", "validation")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return
	}

	// Write timestamped file
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	tsPath := filepath.Join(dir, ts+".json")
	if err := os.WriteFile(tsPath, data, 0644); err != nil {
		return
	}

	// Write latest.json
	latestPath := filepath.Join(dir, "latest.json")
	if err := os.WriteFile(latestPath, data, 0644); err != nil {
		return
	}

	// Prune old files (keep newest 20)
	pruneOldLedgers(dir, 20)
}

func pruneOldLedgers(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var jsonFiles []string
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || !strings.HasSuffix(name, ".json") {
			continue
		}
		jsonFiles = append(jsonFiles, name)
	}

	if len(jsonFiles) <= keep {
		return
	}

	sort.Strings(jsonFiles)
	toRemove := jsonFiles[:len(jsonFiles)-keep]
	for _, name := range toRemove {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
