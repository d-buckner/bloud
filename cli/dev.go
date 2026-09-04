// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
	"codeberg.org/d-buckner/bloud/cli/vm"
)

// localExec runs a command on the host machine
func localExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// isSignalExit reports whether err came from a process killed by a signal
// (e.g. Ctrl-C), which interactive commands treat as a clean stop.
func isSignalExit(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == -1 {
		return true
	}
	return false
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

	// Walk up looking for specs/spec.md (project root marker)
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "specs", "spec.md")); err == nil {
			return dir, nil
		}
	}

	return "", fmt.Errorf("could not find project root (looking for specs/spec.md)")
}

func limaInstance() string {
	if v := os.Getenv("BLOUD_E2E_LIMA_INSTANCE"); v != "" {
		return v
	}
	return "bloud-dev"
}

func qemuInstance() string {
	if v := os.Getenv("BLOUD_QEMU_INSTANCE"); v != "" {
		return v
	}
	return "bloud-qemu"
}

// vmInstance returns the instance name for a backend. Native backends have
// no instance; the label is "native".
func vmInstance(name string) string {
	switch name {
	case "qemu":
		return qemuInstance()
	case "native":
		return "native"
	default:
		return limaInstance()
	}
}

// trustedLocalNetsEnv returns the BLOUD_TRUSTED_LOCAL_NETS value for the
// host-agent. QEMU slirp NAT presents host-forwarded connections from the
// gateway (10.0.2.2), not loopback, so the host-agent must trust that subnet
// for host-side API calls (e.g. ./bloud install, e2e). Lima forwards to
// loopback, so no trusted nets are needed.
func trustedLocalNetsEnv(name string) string {
	if name == "qemu" {
		return "10.0.2.0/24"
	}
	return ""
}

// ssoIssuerURL is the OIDC issuer base URL reachable from inside app
// containers: the SSO subdomain of the base domain, mapped to the host
// gateway via per-app extraHosts. An explicit BLOUD_SSO_ISSUER_URL wins.
func ssoIssuerURL() string {
	if v := os.Getenv("BLOUD_SSO_ISSUER_URL"); v != "" {
		return v
	}
	baseDomain := os.Getenv("BLOUD_BASE_DOMAIN")
	if baseDomain == "" {
		baseDomain = "localhost"
	}
	return fmt.Sprintf("http://sso.%s:8080", baseDomain)
}

// vmLabel is the human-readable backend name for a backend.
func vmLabel(name string) string {
	switch name {
	case "qemu":
		return "QEMU VM"
	case "native":
		return "native host"
	default:
		return "Lima VM"
	}
}

// cmdStart prints usage guidance — the real dev loop is ./bloud dev.
func cmdStart() int {
	name, err := backendName()
	if err != nil {
		errorf("No runtime backend: %v", err)
		return 1
	}

	fmt.Println("Start the dev environment:")
	fmt.Println()
	fmt.Println("  ./bloud dev          Build, deploy to runtime VM, and run host-agent (Ctrl-C to stop)")
	fmt.Println()
	switch name {
	case "qemu":
		fmt.Println("Prerequisites (QEMU backend):")
		fmt.Println("  ./bloud dev   # provisions .bloud/qemu/bloud-qemu, boots VM")
	case "native":
		fmt.Println("Prerequisites (native backend):")
		fmt.Println("  ./bloud setup  # checks prerequisites; ./bloud dev runs on this host")
	default:
		fmt.Println("Prerequisites (Lima backend):")
		fmt.Println("  limactl create --name=bloud-dev dev/lima.yaml")
		fmt.Println("  limactl start bloud-dev")
	}
	return 0
}

func cmdStop() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	log("Stopping host-agent on " + inst)
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop apps-*.service 2>/dev/null; true`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to stop host-agent: %v", err)
		return 1
	}
	log("Stopped")
	return 0
}

func cmdStatus() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	fmt.Println()
	fmt.Printf("  %s:  %s\n", vmLabel(name), inst)

	host := bk.Host()

	// Check if VM is running
	if host.Ready() {
		fmt.Printf("  VM status: %sRunning%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  VM status: %sStopped%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Start the VM with: ./bloud dev")
		return 0
	}

	// Check host-agent
	res, err := host.Executor().Run(context.Background(), executor.RunSpec{
		Command: `curl -sf http://localhost:3000/api/health 2>/dev/null && echo ok || echo down`,
	})
	if err == nil && strings.Contains(res.Stdout, "ok") {
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
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	log("Streaming host-agent logs from " + inst + " (Ctrl-C to stop)...")
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `journalctl --user -u host-agent -f 2>/dev/null || journalctl -f 2>/dev/null`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to stream logs: %v", err)
		return 1
	}
	return 0
}

func cmdAttach() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	sshex, ok := bk.Host().Executor().(*executor.SSHExecutor)
	if !ok {
		errorf("Backend host does not support interactive shells")
		return 1
	}
	log("Opening shell on " + inst + " (type 'exit' to leave)...")
	if err := sshex.InteractiveShell(context.Background(), os.Stdout, os.Stderr, os.Stdin); err != nil && !isSignalExit(err) {
		errorf("Failed to open shell on "+inst+": %v", err)
		return 1
	}
	return 0
}

func cmdShell(args []string) int {
	if len(args) == 0 {
		return cmdAttach()
	}
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	command := strings.Join(args, " ")
	if err := bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: command,
	}, os.Stdout, os.Stderr); err != nil && !isSignalExit(err) {
		errorf("Command failed: %v", err)
		return 1
	}
	return 0
}

func cmdRebuild() int {
	fmt.Println("'rebuild' is not supported (Nix runtime was removed).")
	fmt.Println()
	fmt.Println("To pick up code changes, re-run: ./bloud dev")
	return 0
}

func cmdServices() int {
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `podman ps --all --filter 'name=^apps-' --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to list services: %v", err)
		return 1
	}
	return 0
}

func cmdReset() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	host := bk.Host()
	ex := host.Executor()
	dirs := host.DataDirs()

	fmt.Printf("This will stop all services and wipe all app data in '%s'.\n", inst)
	fmt.Printf("The VM itself is kept — only data, containers, and the database are removed.\n")
	fmt.Print("Continue? [y/N] ")
	resp, _ := stdinReader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(resp)) != "y" {
		fmt.Println("Aborted.")
		return 0
	}

	// 1. Kill host-agent and any app systemd units
	log("Stopping host-agent")
	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop 'apps-*.service' 'bloud-e2e-host-agent.service' 2>/dev/null; true`,
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to stop host-agent: %v", err)
		return 1
	}

	// 2. Remove all containers
	log("Removing containers")
	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: `
set -e
podman rm -f $(podman ps -aq) 2>/dev/null || true
podman system prune -f 2>/dev/null || true
`,
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to stop services: %v", err)
		return 1
	}

	// 3. Wipe data directories and database. The runtime data dir is
	// backend-specific (Lima: /var/tmp/bloud-dev-runtime/data, QEMU:
	// /var/tmp/bloud-qemu-runtime/data); the bloud.db database lives inside
	// it (BLOUD_DATA_DIR). Use podman unshare for dirs with container-owned
	// files (e.g. postgres).
	//
	// $HOME/.local/share/bloud is a real, non-disposable path on native.
	homeWipe := ""
	if name != "native" {
		homeWipe = "podman unshare rm -rf \"$HOME/.local/share/bloud\"\n"
	}
	wipe := fmt.Sprintf("set -e\n%spodman unshare rm -rf %s\nrm -f %s/bloud.db\n", homeWipe, dirs.DataDir, dirs.DataDir)
	log("Wiping data")
	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: wipe,
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to wipe data: %v", err)
		return 1
	}

	log("Reset complete — run ./bloud dev to start fresh")
	return 0
}

func cmdDestroy() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	fmt.Printf("This will stop and delete the %s '%s'.\n", vmLabel(name), inst)
	fmt.Print("Continue? [y/N] ")
	resp, _ := stdinReader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(resp)) != "y" {
		fmt.Println("Aborted.")
		return 0
	}
	if err := bk.Destroy(context.Background()); err != nil {
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

	if httpCode != "200" && httpCode != "201" && httpCode != "202" {
		errorf("Install failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully installed %s", appName))
	fmt.Println(responseBody)
	return 0
}

// devBackend builds the selected backend for the current project and
// returns its name.
func devBackend() (backend.Backend, string, error) {
	root, err := getProjectRoot()
	if err != nil {
		return nil, "", err
	}
	name, err := backendName()
	if err != nil {
		return nil, "", err
	}
	switch name {
	case "qemu":
		return backend.NewQEMUBackend(qemuInstance(), root), name, nil
	case "native":
		return backend.NewNativeBackend(root), name, nil
	default:
		return backend.NewLimaBackend(limaInstance(), root), name, nil
	}
}

func cmdDev() int {
	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}

	// Provision the VM if it is not already running. This is a no-op when the
	// guest is already up (Lima: already created+started; QEMU: image+seed
	// present and guest reachable), so it is safe for both backends.
	log("Provisioning " + vmLabel(name))
	if err := bk.Create(context.Background()); err != nil {
		errorf("Failed to provision VM: %v", err)
		return 1
	}

	host := bk.Host()
	ex := host.Executor()
	dirs := host.DataDirs()
	goarch := runtime.GOARCH

	// Clean slate: remove managed containers before the host-agent takes over.
	// Also remove any stale legacy dev containers (bloud-dev-postgres,
	// bloud-dev-redis, dev_* compose names) that predate the host-agent
	// self-bootstrap. There is no shared postgres/redis compose stack anymore —
	// apps own their infra containers (e.g. apps-authentik-postgres) via
	// metadata.yaml containers blocks, so the host-agent is the single manager.
	// apps-traefik is included because it uses host network and holds port 8080.
	log("Stopping managed app containers")
	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: `podman rm -f bloud-dev-postgres bloud-dev-redis apps-traefik dev_authentik-worker_1 dev_authentik-proxy_1 apps-authentik-ldap apps-authentik-server 2>/dev/null; podman ps -a --filter label=io.bloud.managed=true -q | xargs -r podman rm -f -t 2 2>/dev/null; true`,
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to stop managed app containers: %v", err)
		return 1
	}

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
	log("Deploying to " + dirs.HostAgentDir)
	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: "mkdir -p " + dirs.HostAgentDir,
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to create remote dir: %v", err)
		return 1
	}
	if err := ex.CopyTo(context.Background(), binaryPath, dirs.HostAgentDir+"/host-agent"); err != nil {
		errorf("Failed to copy binary: %v", err)
		return 1
	}

	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: "chmod 755 " + dirs.HostAgentDir + "/host-agent",
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to chmod binary: %v", err)
		return 1
	}

	// Deploy frontend build to VM
	webBuildDir := filepath.Join(webDir, "build")
	if _, err := os.Stat(webBuildDir); err == nil {
		if err := ex.RunStream(context.Background(), executor.RunSpec{
			Command: "rm -rf " + dirs.HostAgentDir + "/web/build",
		}, os.Stdout, os.Stderr); err != nil {
			errorf("Failed to clean remote web dir: %v", err)
			return 1
		}
		if err := ex.RunStream(context.Background(), executor.RunSpec{
			Command: "mkdir -p " + dirs.HostAgentDir + "/web",
		}, os.Stdout, os.Stderr); err != nil {
			errorf("Failed to create remote web dir: %v", err)
			return 1
		}
		if err := ex.CopyTo(context.Background(), webBuildDir, dirs.HostAgentDir+"/web/build"); err != nil {
			errorf("Failed to copy frontend build: %v", err)
			return 1
		}
		log("Frontend deployed")
	}

	// Kill anything on port 3000 and any previous dev host-agent
	if err := ex.RunStream(context.Background(), executor.RunSpec{
		Command: `fuser -k 3000/tcp 2>/dev/null || true; pkill -f '` + dirs.HostAgentDir + `/host-agent/host-a[g]ent' 2>/dev/null || true; sleep 0.5`,
	}, os.Stdout, os.Stderr); err != nil {
		errorf("Failed to stop previous host-agent: %v", err)
		return 1
	}

	// Run foreground. The SSO issuer URL is derived (see ssoIssuerURL); all
	// other configuration resolves through env vars, secrets.json, and the
	// host-agent's dev fallbacks.
	log("Starting host-agent (Ctrl-C to stop)")
	runEnv := map[string]string{
		"BLOUD_DATA_DIR":            dirs.DataDir,
		"BLOUD_APPS_DIR":            dirs.AppsDir,
		"BLOUD_TRAEFIK_DYNAMIC_DIR": dirs.DataDir + "/traefik/dynamic",
		"BLOUD_TRUSTED_LOCAL_NETS":  trustedLocalNetsEnv(name),
		"BLOUD_SSO_ISSUER_URL":      ssoIssuerURL(),
	}
	runErr := ex.RunStream(context.Background(), executor.RunSpec{
		Command: "unset DATABASE_URL; exec ./host-agent",
		Dir:     dirs.HostAgentDir,
		Env:     runEnv,
	}, os.Stdout, os.Stderr)
	if runErr != nil && !isSignalExit(runErr) {
		errorf("host-agent exited: %v", runErr)
		return 1
	}
	return 0
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

	if httpCode != "200" && httpCode != "202" {
		errorf("Uninstall failed (HTTP %s): %s", httpCode, responseBody)
		return 1
	}

	log(fmt.Sprintf("Successfully uninstalled %s", appName))
	fmt.Println(responseBody)
	return 0
}
