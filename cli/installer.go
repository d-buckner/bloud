package main

import (
	"fmt"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

const installerTmuxSession = "bloud-installer"

func cmdInstaller() int {
	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Check if already running
	output, _ := vm.LocalExec(fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", installerTmuxSession))
	if strings.TrimSpace(output) == "running" {
		log("Installer already running!")
		fmt.Println()
		fmt.Println("  http://localhost:5174     Installer UI")
		fmt.Println("  http://localhost:3001     Installer API (mock)")
		fmt.Println()
		fmt.Printf("  ./bloud installer stop   Stop the installer dev server\n")
		fmt.Printf("  tmux attach -t %s   Attach to session\n", installerTmuxSession)
		return 0
	}

	installerDir := projectRoot + "/services/installer"
	webDir := installerDir + "/web"

	// Ensure installer web deps are installed
	if _, err := vm.LocalExec("ls " + webDir + "/node_modules 2>/dev/null"); err != nil {
		log("Installing installer web dependencies...")
		if err := vm.LocalExecStream("npm install --workspace=@bloud/installer-web"); err != nil {
			warn(fmt.Sprintf("npm install failed: %v", err))
		}
	}

	// Pane 0: installer backend with mock mode
	backendCmd := fmt.Sprintf("cd %s && INSTALLER_MOCK=1 go run ./cmd/installer", installerDir)

	// Pane 1: installer frontend vite dev server
	frontendCmd := fmt.Sprintf("cd %s && npm run dev --workspace=@bloud/installer-web", projectRoot)

	if _, err := vm.LocalExec(fmt.Sprintf("tmux new-session -d -s %s -n installer", installerTmuxSession)); err != nil {
		errorf("Failed to create tmux session: %v", err)
		return 1
	}

	_, _ = vm.LocalExec(fmt.Sprintf("tmux send-keys -t %s:installer '%s' Enter", installerTmuxSession, backendCmd))
	_, _ = vm.LocalExec(fmt.Sprintf("tmux split-window -h -t %s:installer", installerTmuxSession))
	_, _ = vm.LocalExec(fmt.Sprintf("tmux send-keys -t %s:installer.1 '%s' Enter", installerTmuxSession, frontendCmd))
	_, _ = vm.LocalExec(fmt.Sprintf("tmux select-layout -t %s:installer even-horizontal", installerTmuxSession))

	fmt.Println()
	fmt.Println("=== Installer Dev Server Started ===")
	fmt.Println()
	fmt.Println("  http://localhost:5174     Installer UI")
	fmt.Println("  http://localhost:3001     Installer API (mock)")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Printf("  tmux attach -t %s   View both processes (Ctrl-B D to detach)\n", installerTmuxSession)
	fmt.Printf("  ./bloud installer stop      Stop the installer dev server\n")
	fmt.Println()

	return 0
}

func cmdInstallerStop() int {
	output, _ := vm.LocalExec(fmt.Sprintf("tmux has-session -t %s 2>/dev/null && echo running || echo stopped", installerTmuxSession))
	if strings.TrimSpace(output) != "running" {
		log("Installer is not running")
		return 0
	}

	_, _ = vm.LocalExec(fmt.Sprintf("tmux kill-session -t %s", installerTmuxSession))
	log("Installer dev server stopped")
	return 0
}
