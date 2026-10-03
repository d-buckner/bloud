// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
)

// devFlags are the options the dev loop understands.
type devFlags struct {
	// watch selects the hot-reload loop. It is the default because the
	// one-shot loop is the slow and destructive one; --no-watch asks for it
	// explicitly.
	watch bool
	// reset wipes the runtime (the same wipe as `./bloud reset -y`) before
	// the dev loop starts, so the stack comes up from empty data.
	reset bool
	// verbose streams the raw subprocess output to the console instead of
	// quieting it to the dev log. It is the escape hatch for "the summary is
	// not enough", and it changes nothing about what gets logged.
	verbose bool
}

// parseDevFlags parses the dev command's flags. Unknown flags are an error:
// --reset is destructive, so a typo that silently failed to wipe would leave
// the caller believing they were starting from a clean runtime.
func parseDevFlags(args []string) (devFlags, error) {
	flags := devFlags{watch: true}
	for _, arg := range args {
		switch arg {
		case "--watch":
			flags.watch = true
		case "--no-watch":
			flags.watch = false
		case "--reset":
			flags.reset = true
		case "-v", "--verbose":
			flags.verbose = true
		default:
			return flags, fmt.Errorf("unknown dev flag %q (expected --watch, --no-watch, --reset, or --verbose)", arg)
		}
	}
	if v := os.Getenv("BLOUD_DEV_VERBOSE"); v == "1" || strings.EqualFold(v, "true") {
		flags.verbose = true
	}
	return flags, nil
}

// cmdDev is the whole dev loop. By default it hot-reloads: the host-agent is
// rebuilt and restarted on save while the containers it manages are left
// running, and the dashboard is served by a vite dev server that hot-reloads
// on save. --no-watch runs the old one-shot build-deploy-foreground instead.
// --reset wipes the runtime first (the same wipe as ./bloud reset -y, run
// without a prompt because the flag is the consent) and then starts the loop.
func cmdDev(args []string) int {
	flags, err := parseDevFlags(args)
	if err != nil {
		errorf("%v", err)
		return 1
	}

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

	// The console is built here rather than inside each loop so both paths
	// share one log file, one header, and one close.
	c, err := newDevConsole(os.Stdout, devLogPath(root), flags.verbose)
	if err != nil {
		errorf("could not open the dev log: %v", err)
		return 1
	}
	defer func() { _ = c.Close() }()
	c.Header(vmLabel(name), fmt.Sprintf("http://localhost:%s", portOr(bk.Host().Ports(), "traefik", defaultTraefikPort)))

	if flags.watch {
		// Hot reload needs the CLI to own the host-agent as a child process it
		// can stop and start. That is only true on the native backend, where
		// the CLI and the host-agent share a machine; over SSH the same loop
		// means remote process supervision and a file copy per reload, which is
		// not what was wired. Say so and fall through rather than silently
		// running something different from what was asked for.
		if name == "native" {
			return runDevWatch(root, bk, flags, c)
		}
		c.Note("hot reload is wired for the native backend only; running the one-shot loop")
	}

	return runDevOnce(root, bk, name, flags, c)
}

// resetIfRequested runs the full runtime wipe when --reset was passed. The
// loops call it after provisioning, not before: on a machine whose runtime
// does not exist yet, the wipe's commands have nowhere to execute, so
// resetting first would turn `./bloud dev --reset` into a failure instead of
// a fresh start.
func resetIfRequested(flags devFlags, bk backend.Backend, name string) error {
	if !flags.reset {
		return nil
	}
	log("--reset: wiping the runtime before starting")
	if err := resetRuntime(bk, name); err != nil {
		return fmt.Errorf("reset before dev: %w", err)
	}
	return nil
}

// runDevWatch brings up the hot-reload loop on a native runtime.
func runDevWatch(root string, bk backend.Backend, flags devFlags, c *devConsole) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host := bk.Host()
	ex := host.Executor()
	// Create is a no-op when the runtime is already up, so the step is quick
	// on every launch after the first.
	provStart := time.Now()
	if err := bk.Create(ctx); err != nil {
		c.StepFailed("provision runtime", "", time.Since(provStart), err)
		return 1
	}
	c.Step("provision runtime", "", time.Since(provStart))

	if flags.reset {
		resetStart := time.Now()
		if err := resetRuntime(bk, "native"); err != nil {
			c.StepFailed("wipe runtime", "--reset", time.Since(resetStart), err)
			return 1
		}
		c.Step("wipe runtime", "--reset", time.Since(resetStart))
	}

	dirs := host.DataDirs()
	if !clearDevLoopConflicts(ctx, ex, dirs, c) {
		return 1
	}

	env := devRunEnv(dirs, "native")
	env["BLOUD_DEV_VITE_URL"] = fmt.Sprintf("http://%s:%d", devViteHost, devVitePort)
	env["BLOUD_DEV_FAST_GATE"] = "1"

	pidPath := devLoopPIDPath(dirs.HostAgentDir)
	if err := writeDevLoopPID(pidPath); err != nil {
		c.StepFailed("record the dev loop pid", "", 0, err)
		return 1
	}
	defer func() { _ = os.Remove(pidPath) }()

	// The API opens only once the host-agent is listening, which is the one
	// bring-up fact the steps above cannot report. Watching for it from the
	// side is what turns "host-agent started" into "open your browser".
	go announceHostAgentReady(ctx, ex, c, host.Ports(), readyPollInterval, devQuietProgressFirst)

	return runHotReload(ctx, hotReloadOptions{
		root:       root,
		binaryPath: dirs.HostAgentDir + "/host-agent",
		runDir:     dirs.HostAgentDir,
		env:        env,
		console:    c,
	})
}

// clearDevLoopConflicts removes everything a previous run leaves in this
// loop's way. It reports on the console itself and returns false rather than
// an error: each step carries its own label, and the caller only ever stops.
func clearDevLoopConflicts(ctx context.Context, ex executor.Executor, dirs executor.DataDirs, c *devConsole) bool {
	pidPath := devLoopPIDPath(dirs.HostAgentDir)
	// A reload must never wipe the stack it is reloading, so the managed-
	// container sweep the one-shot loop runs is deliberately absent here.
	// Only the pre-catalog legacy names are cleared: nothing reconciles those,
	// so a leftover from an old compose stack would squat on a port forever.
	// Housekeeping like this runs on every launch and is invisible unless it
	// fails, so it writes to the log rather than the console.
	quiet := c.QuietStream()
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: `podman rm -f bloud-dev-postgres bloud-dev-redis dev_authentik-worker_1 dev_authentik-proxy_1 2>/dev/null; true`,
	}, quiet, quiet); err != nil && ctx.Err() == nil {
		c.StepFailed("clear pre-catalog legacy containers", "", 0, err)
		return false
	}

	// A previous loop owns the vite and host-agent children this run is about
	// to start. Take it over first so its shutdown path releases those in
	// order, rather than leaving a second loop watching the same ports and
	// respawning a competing dev server.
	tookOver, err := takeoverPreviousDevLoop(pidPath)
	if err != nil {
		c.StepFailed("take over the previous dev loop", "", 0, err)
		return false
	}
	if tookOver {
		c.Note("replacing the dev loop already running (pid file: " + pidPath + ")")
	}

	// Stop a host-agent left behind by an earlier run, or the new one cannot
	// bind its port and the two fight over the same runtime dir.
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: stopPreviousHostAgentCommand("3000", dirs.HostAgentDir+"/host-agent"),
	}, quiet, quiet); err != nil && ctx.Err() == nil {
		c.StepFailed("stop the previous host-agent", "", 0, err)
		return false
	}

	// vite runs on the developer's machine, outside the loop's runtime dir,
	// so a previous run's vite can outlive it and keep the dev-server port.
	// Clear the port before starting ours: the host-agent is told to proxy the
	// dashboard at a fixed port, and a stale server sitting on it serves the
	// dashboard from a checkout that may no longer exist.
	if err := runLocalCommand(ctx, stopPreviousViteCommand(devVitePort)); err != nil && ctx.Err() == nil {
		c.StepFailed("free the dashboard dev-server port", strconv.Itoa(devVitePort), 0, err)
		return false
	}
	return true
}

// devRunEnv is the host-agent's dev environment. The same map feeds the
// one-shot and the hot-reload paths, so the two cannot drift into running with
// different configuration.
func devRunEnv(dirs executor.DataDirs, name string) map[string]string {
	env := map[string]string{
		"BLOUD_DATA_DIR":            dirs.DataDir,
		"BLOUD_APPS_DIR":            dirs.AppsDir,
		"BLOUD_TRAEFIK_DYNAMIC_DIR": dirs.DataDir + "/traefik/dynamic",
		"BLOUD_TRAEFIK_PORT":        traefikPortEnv(name),
		"BLOUD_TRUSTED_LOCAL_NETS":  trustedLocalNetsEnv(name),
		"BLOUD_SSO_ISSUER_URL":      ssoIssuerURL(),
	}
	for k, v := range devPassthrough(os.Getenv) {
		env[k] = v
	}
	return env
}
