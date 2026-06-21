package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

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
			`export BLOUD_RUNTIME=portable `+
			`BLOUD_SYSTEMD_SCOPE=user `+
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
