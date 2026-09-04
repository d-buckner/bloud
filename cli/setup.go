// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func cmdSetup() int {
	fmt.Println()
	fmt.Printf("%s╭──────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│   Bloud Development Setup    │%s\n", colorCyan, colorReset)
	fmt.Printf("%s╰──────────────────────────────╯%s\n", colorCyan, colorReset)
	fmt.Println()

	allGood := true

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	bkName, err := setupBackend(projectRoot)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	prereqs := []prereq{
		{"go", "Go"},
		{"node", "Node.js"},
		{"podman", "Podman"},
	}
	switch bkName {
	case "qemu":
		prereqs = append(prereqs, prereq{"qemu-system-x86_64", "QEMU"})
	case "lima":
		prereqs = append(prereqs, prereq{"limactl", "Lima"})
	}
	missing := checkPrereqs(prereqs)
	allGood = len(missing) == 0

	canAptInstall := bkName == "native" && runtime.GOOS == "linux" && checkCommand("apt-get")
	if !allGood && canAptInstall {
		pkgs := aptPackagesFor(missing)
		if len(pkgs) > 0 {
			fmt.Println()
			fmt.Printf("  Installing missing packages via apt: %s\n", strings.Join(pkgs, " "))
			installCmd := "sudo apt-get update -qq && sudo apt-get install -y -qq " + strings.Join(pkgs, " ")
			cmd := localExec("bash", "-c", installCmd)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				errorf("apt-get install failed: %v", err)
				return 1
			}
			fmt.Println()
			missing = checkPrereqs(prereqs)
			allGood = len(missing) == 0
		}
	}

	fmt.Println()

	if !allGood {
		fmt.Printf("%s✗ Some prerequisites are missing.%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Fix the issues above, then run './bloud setup' again.")
		fmt.Println()
		return 1
	}

	if bkName == "native" {
		if err := ensureSubuidSubgid(); err != nil {
			errorf("%v", err)
			return 1
		}
	}

	// Build CLI binary

	fmt.Print("  Building CLI binary...        ")
	buildCmd := fmt.Sprintf("cd %s/cli && go build -o ../bloud .", projectRoot)
	cmd := localExec("bash", "-c", buildCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("%s✗ failed%s\n", colorRed, colorReset)
		return 1
	}
	fmt.Printf("%s✓ built%s\n", colorGreen, colorReset)

	fmt.Println()
	fmt.Printf("%s✓ Setup complete!%s\n", colorGreen, colorReset)
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("    ./bloud dev")
	fmt.Println()
	return 0
}

func checkCommand(name string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

type prereq struct{ name, label string }

func checkPrereqs(prereqs []prereq) []prereq {
	var missing []prereq
	for _, tool := range prereqs {
		fmt.Printf("  Checking %s...%s", tool.label, strings.Repeat(" ", 22-len(tool.label)))
		if checkCommand(tool.name) {
			fmt.Printf("%s✓ installed%s\n", colorGreen, colorReset)
		} else {
			fmt.Printf("%s✗ not installed%s\n", colorRed, colorReset)
			missing = append(missing, tool)
		}
	}
	return missing
}

var aptPackageNames = map[string][]string{
	"go":     {"golang-go"},
	"node":   {"nodejs", "npm"},
	"podman": {"podman"},
}

func aptPackagesFor(missing []prereq) []string {
	seen := map[string]bool{}
	var pkgs []string
	for _, tool := range missing {
		for _, pkg := range aptPackageNames[tool.name] {
			if !seen[pkg] {
				seen[pkg] = true
				pkgs = append(pkgs, pkg)
			}
		}
	}
	sort.Strings(pkgs)
	return pkgs
}

func ensureSubuidSubgid() error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("could not determine current user: %w", err)
	}

	hasEntry := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, u.Username+":") {
				return true
			}
		}
		return false
	}

	if hasEntry("/etc/subuid") && hasEntry("/etc/subgid") {
		return nil
	}

	fmt.Printf("  Adding subuid/subgid range for %s (needed for rootless Podman)...\n", u.Username)
	cmd := exec.Command("sudo", "usermod", "--add-subuids", "100000-165535", "--add-subgids", "100000-165535", u.Username)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("usermod --add-subuids/--add-subgids failed: %w", err)
	}
	fmt.Println("  Note: a fresh login session (or reboot) may be needed for the new subuid/subgid")
	fmt.Println("  range and linger/podman.socket setup to take effect.")
	return nil
}

