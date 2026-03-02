package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

func cmdSetup() int {
	return cmdSetupNative()
}

func cmdSetupNative() int {
	fmt.Println()
	fmt.Printf("%s╭─────────────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│   Bloud Development Setup (NixOS)   │%s\n", colorCyan, colorReset)
	fmt.Printf("%s╰─────────────────────────────────────╯%s\n", colorCyan, colorReset)
	fmt.Println()

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	allGood := true

	// Check prerequisites
	for _, tool := range []struct{ name, label string }{
		{"go", "Go"},
		{"node", "Node.js"},
		{"tmux", "tmux"},
		{"podman", "Podman"},
	} {
		fmt.Printf("  Checking %s...%s", tool.label, strings.Repeat(" ", 22-len(tool.label)))
		if checkCommand(tool.name) {
			fmt.Printf("%s✓ installed%s\n", colorGreen, colorReset)
		} else {
			fmt.Printf("%s✗ not installed%s\n", colorRed, colorReset)
			fmt.Printf("     Ensure %s is available in your NixOS configuration\n", tool.name)
			allGood = false
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

	// Build host-agent binary
	fmt.Print("  Building host-agent...        ")
	buildCmd := fmt.Sprintf("cd %s/services/host-agent && go build -o /tmp/host-agent ./cmd/host-agent", projectRoot)
	if err := vm.LocalExecStream(buildCmd); err != nil {
		fmt.Printf("%s✗ failed%s\n", colorRed, colorReset)
		errorf("Failed to build host-agent: %v", err)
		return 1
	}
	fmt.Printf("%s✓ built%s\n", colorGreen, colorReset)

	// Initialize secrets
	fmt.Print("  Initializing secrets...       ")
	if _, err := vm.LocalExec(fmt.Sprintf("/tmp/host-agent init-secrets %s", nativeDataDir)); err != nil {
		fmt.Printf("%s✗ failed%s\n", colorRed, colorReset)
		errorf("Failed to initialize secrets: %v", err)
		return 1
	}
	fmt.Printf("%s✓ done%s\n", colorGreen, colorReset)

	// Check if NixOS config needs rebuild
	fmt.Print("  Checking NixOS config...      ")
	output, err := vm.LocalExec("systemctl --user cat bloud-apps.target 2>/dev/null")
	needsRebuild := err != nil || !strings.Contains(output, "[Unit]")

	if needsRebuild {
		fmt.Printf("%s○ needs update%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Rebuilding NixOS configuration (this may take a few minutes)...")
		fmt.Println()
		rebuildCmd := fmt.Sprintf("sudo nixos-rebuild switch --flake %s#dev-server --impure", projectRoot)
		if err := vm.LocalExecStream(rebuildCmd); err != nil {
			errorf("NixOS rebuild failed: %v", err)
			return 1
		}
		fmt.Println()
		fmt.Printf("  NixOS config:                 %s✓ rebuilt%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("%s✓ up to date%s\n", colorGreen, colorReset)
	}

	fmt.Println()
	fmt.Printf("%s✓ Setup complete!%s\n", colorGreen, colorReset)
	fmt.Println()
	fmt.Println("  Run './bloud start' to start the development environment.")
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
