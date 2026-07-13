package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

// localExec runs a command on the host machine
func localExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func getProjectRoot() (string, error) {
	// Find project root by looking for cli/main.go relative to executable or cwd
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Check if we're in the project root
	if _, err := os.Stat(filepath.Join(cwd, "cli", "main.go")); err == nil {
		return cwd, nil
	}

	// Check if we're in cli directory
	if _, err := os.Stat(filepath.Join(cwd, "main.go")); err == nil {
		return filepath.Dir(cwd), nil
	}

	// Walk up looking for SPEC.md (project root marker)
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "SPEC.md")); err == nil {
			return dir, nil
		}
	}

	return "", fmt.Errorf("could not find project root (looking for SPEC.md)")
}

func limaInstance() string {
	if v := os.Getenv("BLOUD_E2E_LIMA_INSTANCE"); v != "" {
		return v
	}
	return "bloud-dev"
}

// cmdStart prints usage guidance — the real dev loop is ./bloud dev.
func cmdStart() int {
	fmt.Println("Start the dev environment:")
	fmt.Println()
	fmt.Println("  ./bloud dev          Build, deploy to Lima VM, and run host-agent (Ctrl-C to stop)")
	fmt.Println()
	fmt.Println("Prerequisites:")
	fmt.Println("  limactl create --name=bloud-dev dev/lima.yaml")
	fmt.Println("  limactl start bloud-dev")
	fmt.Println("  limactl shell bloud-dev bash dev/setup.sh")
	return 0
}

func cmdStop() int {
	lima := limaInstance()
	log("Stopping host-agent on " + lima)
	cmd := exec.Command("limactl", "shell", lima, "bash", "-c",
		`pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop apps-*.service 2>/dev/null; true`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	log("Stopped")
	return 0
}

func cmdStatus() int {
	lima := limaInstance()
	fmt.Println()
	fmt.Printf("  Lima VM:  %s\n", lima)

	// Check if VM is running
	out, err := exec.Command("limactl", "list", "--json").Output()
	if err == nil {
		if strings.Contains(string(out), `"name":"`+lima+`"`) && strings.Contains(string(out), `"status":"Running"`) {
			fmt.Printf("  VM status: %sRunning%s\n", colorGreen, colorReset)
		} else {
			fmt.Printf("  VM status: %sStopped%s\n", colorRed, colorReset)
			fmt.Println()
			fmt.Println("  Start the VM with: limactl start " + lima)
			return 0
		}
	}

	// Check host-agent
	curl := exec.Command("limactl", "shell", lima, "bash", "-c",
		"curl -sf http://localhost:3000/api/health 2>/dev/null && echo ok || echo down")
	if o, err := curl.Output(); err == nil && strings.TrimSpace(string(o)) == "ok" {
		fmt.Printf("  Host agent: %sRunning%s (localhost:3000)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Host agent: %sNot running%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run: ./bloud dev")
	}

	fmt.Println()
	return 0
}

func cmdLogs() int {
	lima := limaInstance()
	log("Streaming host-agent logs from " + lima + " (Ctrl-C to stop)...")
	cmd := exec.Command("limactl", "shell", lima, "bash", "-c",
		"journalctl --user -u host-agent -f 2>/dev/null || journalctl -f 2>/dev/null")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	return 0
}

func cmdAttach() int {
	lima := limaInstance()
	log("Opening shell on " + lima + " (type 'exit' to leave)...")
	cmd := exec.Command("limactl", "shell", lima)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
	return 0
}

func cmdShell(args []string) int {
	lima := limaInstance()
	if len(args) == 0 {
		return cmdAttach()
	}
	command := strings.Join(args, " ")
	if err := vm.LocalInteractive(fmt.Sprintf("limactl shell %s bash -c %s", lima, shellQuoteArg(command))); err != nil {
		errorf("Command failed: %v", err)
		return 1
	}
	return 0
}

func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func cmdRebuild() int {
	fmt.Println("'rebuild' is not supported (Nix runtime was removed).")
	fmt.Println()
	fmt.Println("To pick up code changes, re-run: ./bloud dev")
	return 0
}

func cmdServices() int {
	lima := limaInstance()
	cmd := exec.Command("limactl", "shell", lima, "bash", "-c",
		"systemctl --user list-units 'apps-*' --all --no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
	return 0
}

func cmdReset() int {
	lima := limaInstance()

	fmt.Printf("This will stop all services and wipe all app data in '%s'.\n", lima)
	fmt.Printf("The VM itself is kept — only data, containers, and the database are removed.\n")
	fmt.Print("Continue? [y/N] ")
	var resp string
	fmt.Scanln(&resp)
	if strings.ToLower(strings.TrimSpace(resp)) != "y" {
		fmt.Println("Aborted.")
		return 0
	}

	// 1. Kill host-agent
	log("Stopping host-agent")
	cmd := exec.Command("limactl", "shell", lima, "bash", "-c",
		`pkill -f 'host-agent$' 2>/dev/null; true`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// 2. Stop all app services and remove containers + quadlet files
	log("Stopping services and removing containers")
	cmd = exec.Command("limactl", "shell", lima, "bash", "-c", `
set -e
systemctl --user stop apps-*.service 2>/dev/null || true
podman rm -f $(podman ps -aq) 2>/dev/null || true
systemctl --user reset-failed 2>/dev/null || true
rm -f "$HOME/.config/containers/systemd"/apps-*.container 2>/dev/null || true
systemctl --user daemon-reload 2>/dev/null || true
`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// 3. Wipe data directories and database
	// Use podman unshare for dirs with container-owned files (e.g. postgres)
	log("Wiping data")
	cmd = exec.Command("limactl", "shell", lima, "bash", "-c", `
set -e
podman unshare rm -rf "$HOME/.local/share/bloud"
podman unshare rm -rf /var/tmp/bloud-dev-runtime/data
rm -f /var/tmp/bloud-dev-runtime/bloud.db
`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		errorf("Failed to wipe data: %v", err)
		return 1
	}

	log("Reset complete — run ./bloud dev to start fresh")
	return 0
}

func cmdDestroy() int {
	lima := limaInstance()
	fmt.Printf("This will stop and delete the Lima VM '%s'.\n", lima)
	fmt.Print("Continue? [y/N] ")
	var resp string
	fmt.Scanln(&resp)
	if strings.ToLower(strings.TrimSpace(resp)) != "y" {
		fmt.Println("Aborted.")
		return 0
	}
	log("Deleting " + lima + "...")
	cmd := exec.Command("limactl", "delete", "--force", lima)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		errorf("Failed to delete VM: %v", err)
		return 1
	}
	log("VM deleted")
	return 0
}

func cmdInstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud install <app-name>")
		return 1
	}
	return installApp(3000, args[0])
}

func cmdUninstall(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud uninstall <app-name>")
		return 1
	}
	return uninstallApp(3000, args[0])
}

// installApp calls the host-agent API to install an app
func installApp(apiPort int, appName string) int {
	log(fmt.Sprintf("Installing %s...", appName))

	curlCmd := fmt.Sprintf(`curl -s -X POST -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/install`, apiPort, appName)
	output, err := vm.LocalExec(curlCmd)
	if err != nil {
		errorf("Failed to call install API: %v", err)
		return 1
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" && httpCode != "201" {
		errorf("Install failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully installed %s", appName))
	fmt.Println(responseBody)
	return 0
}

const devRemoteDir = "/var/tmp/bloud-dev-runtime"

func cmdDev() int {
	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	lima := os.Getenv("BLOUD_E2E_LIMA_INSTANCE")
	if lima == "" {
		lima = "bloud-dev"
	}
	goarch := runtime.GOARCH

	// Clean slate: stop systemd-managed services first, then remove containers and quadlet files.
	// Stopping services before removing quadlet files avoids systemd timeouts on restart.
	// Also remove legacy dev containers (bloud-dev-postgres, bloud-dev-redis) that
	// predate the host-agent self-bootstrap and would hold the ports.
	// apps-traefik is included because it uses host network and holds port 8080.
	log("Stopping managed app containers")
	_ = limaRun(lima, `systemctl --user stop apps-*.service 2>/dev/null; podman rm -f bloud-dev-postgres bloud-dev-redis apps-traefik 2>/dev/null; podman ps -a --filter label=io.bloud.managed=true -q | xargs -r podman rm -f -t 2 2>/dev/null; systemctl --user reset-failed 2>/dev/null; rm -f "$HOME/.config/containers/systemd"/apps-*.container 2>/dev/null; systemctl --user daemon-reload 2>/dev/null; true`)

	// Build
	log("Building host-agent for linux/" + goarch)
	tmpDir, err := os.MkdirTemp("", "bloud-dev-build-*")
	if err != nil {
		errorf("Failed to create temp dir: %v", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	hostAgentDir := filepath.Join(root, "services", "host-agent")
	binaryPath := filepath.Join(tmpDir, "host-agent")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/host-agent")
	buildCmd.Dir = hostAgentDir
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		errorf("Build failed: %v", err)
		return 1
	}

	// Build frontend
	log("Building frontend")
	webDir := filepath.Join(hostAgentDir, "web")
	frontendBuild := exec.Command("npm", "run", "build", "--workspace=@bloud/host-agent-web")
	frontendBuild.Dir = root
	frontendBuild.Stdout = os.Stdout
	frontendBuild.Stderr = os.Stderr
	if err := frontendBuild.Run(); err != nil {
		errorf("Frontend build failed: %v", err)
		return 1
	}

	// Deploy
	log("Deploying to " + lima + ":" + devRemoteDir)
	if err := limaRun(lima, "mkdir -p "+devRemoteDir+"/host-agent"); err != nil {
		errorf("Failed to create remote dir: %v", err)
		return 1
	}
	copyCmd := exec.Command("limactl", "copy", binaryPath, lima+":"+devRemoteDir+"/host-agent/host-agent")
	copyCmd.Stdout = os.Stdout
	copyCmd.Stderr = os.Stderr
	if err := copyCmd.Run(); err != nil {
		errorf("Failed to copy binary: %v", err)
		return 1
	}

	if err := limaRun(lima, "chmod 755 "+devRemoteDir+"/host-agent/host-agent"); err != nil {
		errorf("Failed to chmod binary: %v", err)
		return 1
	}

	// Deploy frontend build to VM
	webBuildDir := filepath.Join(webDir, "build")
	if _, err := os.Stat(webBuildDir); err == nil {
		_ = limaRun(lima, "rm -rf "+devRemoteDir+"/host-agent/web/build")
		if err := limaRun(lima, "mkdir -p "+devRemoteDir+"/host-agent/web"); err != nil {
			errorf("Failed to create remote web dir: %v", err)
			return 1
		}
		cpCmd := exec.Command("limactl", "copy", "-r", webBuildDir, lima+":"+devRemoteDir+"/host-agent/web/build")
		cpCmd.Stdout = os.Stdout
		cpCmd.Stderr = os.Stderr
		if err := cpCmd.Run(); err != nil {
			errorf("Failed to copy frontend build: %v", err)
			return 1
		}
		log("Frontend deployed")
	}

	// Kill anything on port 3000 and any previous dev host-agent
	_ = limaRun(lima, `fuser -k 3000/tcp 2>/dev/null || true; pkill -f '`+devRemoteDir+`/host-agent/host-agent' 2>/dev/null || true; sleep 0.5`)

	// Build the env and exec command
	appsDir := filepath.Join(root, "apps")

	remoteScript := fmt.Sprintf(
		`unset DATABASE_URL BLOUD_REDIS_ADDR; `+
			`export BLOUD_SYSTEMD_SCOPE=user `+
			`BLOUD_DATA_DIR=%s/data `+
			`BLOUD_APPS_DIR=%s `+
			`BLOUD_QUADLET_DIR=$HOME/.config/containers/systemd `+
			`BLOUD_TRAEFIK_DYNAMIC_DIR=%s/data/traefik/dynamic; `+
			`cd %s/host-agent && exec ./host-agent`,
		devRemoteDir, appsDir, devRemoteDir, devRemoteDir,
	)

	// Run foreground
	log("Starting host-agent (Ctrl-C to stop)")
	runCmd := exec.Command("limactl", "shell", "--start", lima, "bash", "-c", remoteScript)
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	runCmd.Stdin = os.Stdin
	if err := runCmd.Run(); err != nil {
		// Don't report error on signal-based exit (Ctrl-C)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == -1 {
			fmt.Println()
			return 0
		}
		errorf("host-agent exited: %v", err)
		return 1
	}
	return 0
}

func limaRun(instance, script string) error {
	cmd := exec.Command("limactl", "shell", "--start", instance, "bash", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// uninstallApp calls the host-agent API to uninstall an app
func uninstallApp(apiPort int, appName string) int {
	log(fmt.Sprintf("Uninstalling %s...", appName))

	curlCmd := fmt.Sprintf(`curl -s -X POST -w "\n%%{http_code}" http://localhost:%d/api/apps/%s/uninstall`, apiPort, appName)
	output, err := vm.LocalExec(curlCmd)
	if err != nil {
		errorf("Failed to call uninstall API: %v", err)
		return 1
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		errorf("Empty response from API")
		return 1
	}

	httpCode := lines[len(lines)-1]
	responseBody := strings.Join(lines[:len(lines)-1], "\n")

	if httpCode != "200" {
		errorf("Uninstall failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully uninstalled %s", appName))
	fmt.Println(responseBody)
	return 0
}
