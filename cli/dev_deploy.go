// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
)

// runDevOnce is the one-shot loop: build, deploy, run in the foreground.
func runDevOnce(root string, bk backend.Backend, name string, flags devFlags, c *devConsole) int {
	host := bk.Host()
	ex := host.Executor()
	dirs := host.DataDirs()
	goarch := runtime.GOARCH
	// Most of this loop is routine work whose output only matters when it
	// fails, so it is mirrored to the dev log rather than the console.
	quiet := c.QuietStream()
	ctx := context.Background()

	if err := devProvision(ctx, bk, name, flags, c); err != nil {
		return 1
	}
	if err := devClearContainers(ctx, ex, c); err != nil {
		return 1
	}

	tmpDir, err := os.MkdirTemp("", "bloud-dev-build-*")
	if err != nil {
		c.StepFailed("create the build dir", "", 0, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binaryPath, err := devBuildHostAgent(root, tmpDir, goarch, c)
	if err != nil {
		return 1
	}
	if err := devBuildDashboard(root, c); err != nil {
		return 1
	}

	// Stop any previous host-agent before deploying: copying over a running
	// binary fails with "text file busy" (and by now its containers are gone).
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: stopPreviousHostAgentCommand("3000", dirs.HostAgentDir+"/host-agent"),
	}, quiet, quiet); err != nil {
		c.StepFailed("stop the previous host-agent", "", 0, err)
		return 1
	}

	if err := devDeployHostAgent(ctx, ex, dirs, binaryPath, c); err != nil {
		return 1
	}
	if err := devDeployDashboard(ctx, ex, dirs, filepath.Join(root, devWebBuildRel), c); err != nil {
		return 1
	}

	c.Blank()
	c.Note("running in the foreground · Ctrl-C to stop")
	c.HintLogPath()
	c.Blank()

	if err := devRunForeground(ex, host, dirs, name, c); err != nil {
		return 1
	}
	return 0
}

// devRunForeground runs the host-agent in the foreground until it exits or a
// signal arrives. The SSO issuer URL is derived (see ssoIssuerURL); all other
// configuration resolves through env vars, secrets.json, and the host-agent's
// dev fallbacks.
func devRunForeground(ex executor.Executor, host executor.Host, dirs executor.DataDirs, name string, c *devConsole) error {
	agentTee := c.HostAgentStream()
	// The host-agent opens its API only after every installed app is up, so
	// watch for that and say so instead of leaving the terminal silent.
	readyCtx, stopReadyWatch := context.WithCancel(context.Background())
	go announceHostAgentReady(readyCtx, ex, c, host.Ports(), readyPollInterval, devQuietProgressFirst)
	runErr := ex.RunStream(context.Background(), executor.RunSpec{
		Command: "unset DATABASE_URL; exec ./host-agent",
		Dir:     dirs.HostAgentDir,
		Env:     devRunEnv(dirs, name),
	}, agentTee, agentTee)
	stopReadyWatch()
	if runErr != nil && !isSignalExit(runErr) {
		c.StepFailed("host-agent", "", 0, runErr)
		return runErr
	}
	return nil
}

// devWebBuildRel is where the dashboard build lands, relative to the repo root.
const devWebBuildRel = "services/host-agent/web/build"

// devProvision brings the runtime up and wipes it if asked. Provisioning is a
// no-op when the guest is already running (Lima: created and started; QEMU:
// image and seed present and the guest reachable), so it is safe for both
// backends on every launch.
func devProvision(ctx context.Context, bk backend.Backend, name string, flags devFlags, c *devConsole) error {
	start := time.Now()
	if err := bk.Create(ctx); err != nil {
		c.StepFailed("provision runtime", "", time.Since(start), err)
		return err
	}
	c.Step("provision runtime", "", time.Since(start))

	if !flags.reset {
		return nil
	}
	resetStart := time.Now()
	if err := resetIfRequested(flags, bk, name); err != nil {
		c.StepFailed("wipe runtime", "--reset", time.Since(resetStart), err)
		return err
	}
	c.Step("wipe runtime", "--reset", time.Since(resetStart))
	return nil
}

// devClearContainers gives the host-agent a clean slate before it takes over.
// It also removes the stale legacy dev containers (bloud-dev-postgres,
// bloud-dev-redis, dev_* compose names) that predate the host-agent's
// self-bootstrap. There is no shared postgres/redis compose stack anymore: apps
// own their infra containers (e.g. apps-authentik-postgres) through their
// metadata.yaml containers blocks, so the host-agent is the single manager.
// apps-traefik is included because it uses host network and holds port 80.
func devClearContainers(ctx context.Context, ex executor.Executor, c *devConsole) error {
	quiet := c.QuietStream()
	err := ex.RunStream(ctx, executor.RunSpec{
		Command: `podman rm -f bloud-dev-postgres bloud-dev-redis apps-traefik dev_authentik-worker_1 dev_authentik-proxy_1 apps-authentik-ldap apps-authentik-server 2>/dev/null; podman ps -a --filter label=io.bloud.managed=true -q | xargs -r podman rm -f -t 2 2>/dev/null; true`,
	}, quiet, quiet)
	if err != nil {
		c.StepFailed("stop managed app containers", "", 0, err)
	}
	return err
}

// devBuildHostAgent cross-compiles the host-agent for the guest into tmpDir and
// returns the binary's path.
func devBuildHostAgent(root, tmpDir, goarch string, c *devConsole) (string, error) {
	binaryPath := filepath.Join(tmpDir, "host-agent")
	tee := c.QuietStream()
	start := time.Now()
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/host-agent")
	cmd.Dir = filepath.Join(root, "services", "host-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	cmd.Stdout = tee
	cmd.Stderr = tee
	if err := cmd.Run(); err != nil {
		c.StepFailed("build host-agent", "linux/"+goarch, time.Since(start), err)
		c.PrintTail(tee.Tail(devTailLines))
		return "", err
	}
	c.Step("build host-agent", "linux/"+goarch, time.Since(start))
	return binaryPath, nil
}

// devBuildDashboard produces the static bundle host-agent serves.
func devBuildDashboard(root string, c *devConsole) error {
	tee := c.QuietStream()
	start := time.Now()
	cmd := exec.Command("npm", "run", "build", "--workspace=@bloud/host-agent-web")
	cmd.Dir = root
	cmd.Stdout = tee
	cmd.Stderr = tee
	if err := cmd.Run(); err != nil {
		c.StepFailed("build dashboard", "", time.Since(start), err)
		c.PrintTail(tee.Tail(devTailLines))
		return err
	}
	c.Step("build dashboard", "", time.Since(start))
	return nil
}

// devDeployHostAgent puts the binary in the runtime dir and marks it runnable.
func devDeployHostAgent(ctx context.Context, ex executor.Executor, dirs executor.DataDirs, binaryPath string, c *devConsole) error {
	quiet := c.QuietStream()
	start := time.Now()
	fail := func(err error) error {
		c.StepFailed("deploy host-agent", dirs.HostAgentDir, time.Since(start), err)
		return err
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "mkdir -p " + dirs.HostAgentDir}, quiet, quiet); err != nil {
		return fail(err)
	}
	if err := ex.CopyTo(ctx, binaryPath, dirs.HostAgentDir+"/host-agent"); err != nil {
		return fail(err)
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "chmod 755 " + dirs.HostAgentDir + "/host-agent"}, quiet, quiet); err != nil {
		return fail(err)
	}
	c.Step("deploy host-agent", dirs.HostAgentDir, time.Since(start))
	return nil
}

// devDeployDashboard replaces the bundle the runtime serves. A build that was
// never produced is not a failure here: host-agent falls back to its embedded
// placeholder page, which is the honest thing to show when there is no bundle.
func devDeployDashboard(ctx context.Context, ex executor.Executor, dirs executor.DataDirs, webBuildDir string, c *devConsole) error {
	if _, statErr := os.Stat(webBuildDir); statErr != nil {
		return nil
	}
	quiet := c.QuietStream()
	start := time.Now()
	fail := func(err error) error {
		c.StepFailed("deploy dashboard", "", time.Since(start), err)
		return err
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "rm -rf " + dirs.HostAgentDir + "/web/build"}, quiet, quiet); err != nil {
		return fail(err)
	}
	if err := ex.RunStream(ctx, executor.RunSpec{Command: "mkdir -p " + dirs.HostAgentDir + "/web"}, quiet, quiet); err != nil {
		return fail(err)
	}
	if err := ex.CopyTo(ctx, webBuildDir, dirs.HostAgentDir+"/web/build"); err != nil {
		return fail(err)
	}
	c.Step("deploy dashboard", "", time.Since(start))
	return nil
}
