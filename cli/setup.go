// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func cmdSetup() int {
	fmt.Println()
	fmt.Printf("%s╭──────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│   Bloud Development Setup    │%s\n", colorCyan, colorReset)
	fmt.Printf("%s╰──────────────────────────────╯%s\n", colorCyan, colorReset)
	fmt.Println()

	allGood := true

	// Check prerequisites. Lima is only needed for the Lima backend
	// (macOS); the QEMU backend (Linux) needs qemu-system-x86_64 instead.
	prereqs := []struct{ name, label string }{
		{"go", "Go"},
		{"node", "Node.js"},
		{"podman", "Podman"},
	}
	if backendName() == "qemu" {
		prereqs = append(prereqs, struct{ name, label string }{"qemu-system-x86_64", "QEMU"})
	} else {
		prereqs = append(prereqs, struct{ name, label string }{"limactl", "Lima"})
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

	// Optional convenience: fetch the Bloud local CA from the VM (when it is
	// running) and print per-OS trust instructions. Never fails setup —
	// trusting the CA on the dev host only suppresses the browser warning on
	// the TLS SSO issuer; the SSO flow works without it.
	trustLocalCA(projectRoot)

	fmt.Println()
	fmt.Printf("%s✓ Setup complete!%s\n", colorGreen, colorReset)
	fmt.Println()
	fmt.Println("  Next steps:")
	if backendName() == "qemu" {
		fmt.Println("    BLOUD_BACKEND=qemu ./bloud dev")
	} else {
		fmt.Println("    limactl create --name=bloud-dev dev/lima.yaml")
		fmt.Println("    limactl start bloud-dev")
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

// trustLocalCA fetches the Bloud local CA certificate from the VM and saves
// it under .bloud/, then prints the one-liner to trust it on this host.
// Best-effort: a stopped VM or a not-yet-bootstrapped runtime simply skips
// the step (the CA is generated on the first host-agent boot).
func trustLocalCA(projectRoot string) {
	bk, err := devBackend()
	if err != nil {
		return
	}
	host := bk.Host()
	remoteCA := filepath.Join(host.DataDirs().DataDir, "tls", "ca.crt")
	localCA := filepath.Join(projectRoot, ".bloud", "ca.crt")
	if err := os.MkdirAll(filepath.Dir(localCA), 0755); err != nil {
		return
	}
	if err := host.Executor().CopyFrom(context.Background(), remoteCA, localCA); err != nil {
		return
	}

	fmt.Println()
	fmt.Println("  Bloud local CA (optional — trust on this machine to skip the")
	fmt.Printf("  browser warning on https://sso.* TLS routes): %s\n", localCA)
	switch runtime.GOOS {
	case "darwin":
		fmt.Println("    sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain .bloud/ca.crt")
	case "linux":
		fmt.Println("    sudo cp .bloud/ca.crt /etc/pki/ca-trust/source/anchors/bloud-local.crt && sudo update-ca-trust")
	default:
		fmt.Println("    import the certificate into your system trust store")
	}
}
