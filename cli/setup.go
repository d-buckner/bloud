package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdSetup() int {
	fmt.Println()
	fmt.Printf("%s╭──────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│   Bloud Development Setup    │%s\n", colorCyan, colorReset)
	fmt.Printf("%s╰──────────────────────────────╯%s\n", colorCyan, colorReset)
	fmt.Println()

	allGood := true

	// Check prerequisites
	prereqs := []struct{ name, label string }{
		{"go", "Go"},
		{"node", "Node.js"},
		{"limactl", "Lima"},
		{"podman", "Podman"},
	}
	if backendName() == "qemu" {
		prereqs = append(prereqs, struct{ name, label string }{"qemu-system-x86_64", "QEMU"})
	}
	for _, tool := range prereqs {
		fmt.Printf("  Checking %s...%s", tool.label, strings.Repeat(" ", 22-len(tool.label)))
		if checkCommand(tool.name) {
			fmt.Printf("%s✓ installed%s\n", colorGreen, colorReset)
		} else {
			fmt.Printf("%s✗ not installed%s\n", colorRed, colorReset)
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

	// Build CLI binary
	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

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
	if backendName() == "qemu" {
		fmt.Println("    BLOUD_BACKEND=qemu ./bloud dev")
	} else {
		fmt.Println("    limactl create --name=bloud-dev dev/lima.yaml")
		fmt.Println("    limactl start bloud-dev")
		fmt.Println("    limactl shell bloud-dev bash dev/setup.sh")
	}
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
