// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// devVitePort is the port the dashboard's dev server listens on. It matches
// `vite dev --port 5173` in the web workspace's package.json. The two are
// kept in step here rather than discovered, because the host-agent has to be
// told where to proxy before vite has told anyone anything.
const devVitePort = 5173

// devViteHost is the address the host-agent proxies the dashboard to. It is a
// literal IPv4 loopback rather than `localhost` on purpose: `localhost` is
// whatever the resolver says it is, and on a dual-stack machine that is
// `::1` first. A vite bound to only one family then decides which server the
// dashboard comes from, and if a stale one holds the other family the proxy
// happily serves the dashboard from that instead. Pinning the family removes
// the ambiguity, and it matches the `host: '0.0.0.0'` vite is configured
// with, which answers on 127.0.0.1.
const devViteHost = "127.0.0.1"

// gracefulStopTimeout bounds how long a reload waits for the old host-agent to
// shut down before killing it. It has to exceed host-agent's own shutdown
// budget, or every reload becomes a forced kill that leaves the orchestrator
// loop mid-write.
const gracefulStopTimeout = 15 * time.Second

// crashBackoff is how long the loop waits before restarting something that
// died on its own. maxCrashes stops the auto-restart after that many
// consecutive deaths: a binary that dies on startup would otherwise be
// relaunched forever, hammering podman and burying its own error under an
// endless restart banner.
const (
	crashBackoff = 2 * time.Second
	maxCrashes   = 2
)

// hotReloadOptions are the paths and environment the watch loop needs.
type hotReloadOptions struct {
	root       string
	binaryPath string
	runDir     string
	env        map[string]string
	console    *devConsole
}

// runHotReload keeps a freshly built host-agent running on top of a stack that
// is already up, with a vite dev server feeding the browser.
//
// The containers are never touched. That is the contract the whole feature is
// built on: a code change swaps the control-plane process, and the next
// reconciliation pass simply runs against the new binary. App containers keep
// running through every reload, so a reload costs seconds instead of a cold
// install, and nothing about a code edit can disturb a running app.
func runHotReload(ctx context.Context, opts hotReloadOptions) int {
	c := opts.console
	if err := os.MkdirAll(opts.runDir, 0o755); err != nil {
		c.StepFailed("prepare the runtime dir", opts.runDir, 0, err)
		return 1
	}

	buildStart := time.Now()
	buildTee := c.QuietStream()
	if err := buildHostAgentTo(opts.root, opts.binaryPath, buildTee, buildTee); err != nil {
		c.StepFailed("build host-agent", "linux/"+runtime.GOARCH, time.Since(buildStart), err)
		c.PrintTail(buildTee.Tail(devTailLines))
		return 1
	}
	c.Step("build host-agent", "linux/"+runtime.GOARCH, time.Since(buildStart))

	// The dashboard's dev server owns its own stream filter: its banner and
	// HMR chatter are noise, a Svelte compile error is not.
	dashTee := c.DashboardStream()
	vite := newRestartableCmd("vite", func() (*exec.Cmd, error) {
		return viteCommand(opts.root, dashTee, dashTee)
	})
	viteDone, err := vite.Start()
	if err != nil {
		c.StepFailed("dashboard dev server", "", 0, err)
		return 1
	}
	c.Step("dashboard dev server", fmt.Sprintf("http://localhost:%d", devVitePort), 0)

	agentTee := c.HostAgentStream()
	child := newRestartableCmd("host-agent", func() (*exec.Cmd, error) {
		return hostAgentCommand(opts, agentTee, agentTee)
	})
	childDone, err := child.Start()
	if err != nil {
		c.StepFailed("host-agent", "", 0, err)
		vite.Stop(gracefulStopTimeout)
		return 1
	}
	c.Step("host-agent", "API http://localhost:3000", 0)

	c.Blank()
	c.Note("watching services/host-agent + apps · Ctrl-C to stop")
	c.HintLogPath()

	changes := make(chan []string, 8)
	go watchBackendSources(ctx, opts.root, changes)

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	loop := &reloadLoop{
		console: c,
		opts:    opts,
		child:   child,
		vite:    vite,
		changes: changes,
		signals: signals,
	}
	return loop.run(ctx, childDone, viteDone)
}

// reloadLoop is the watch loop's event handling, split out from bring-up so
// each handler stays small enough to read on its own.
type reloadLoop struct {
	console *devConsole
	opts    hotReloadOptions
	child   *restartableCmd
	vite    *restartableCmd
	changes <-chan []string
	signals <-chan os.Signal

	crashes int
}

// run consumes events until the context is canceled. The two done channels
// are passed by pointer because each handler replaces them with the next
// child's channel.
func (l *reloadLoop) run(ctx context.Context, childDone, viteDone <-chan struct{}) int {
	l.escalateOnSecondSignal()
	for {
		select {
		case <-ctx.Done():
			l.console.Note("stopping the dev loop")
			l.console.SuppressAgentLog(true)
			l.child.Stop(gracefulStopTimeout)
			l.vite.Stop(gracefulStopTimeout)
			return 0

		case batch := <-l.changes:
			next, ok := l.onChange(batch)
			if !ok {
				continue
			}
			childDone = next

		case <-childDone:
			// A nil channel never fires, so when the crash policy says "stop
			// restarting" the loop simply stops watching this child and waits
			// for a change to try again.
			childDone = l.onChildDeath()

		case <-viteDone:
			next, ok := l.onViteDeath(ctx)
			if !ok {
				continue
			}
			viteDone = next
		}
	}
}

// escalateOnSecondSignal makes a second Ctrl-C (or a second kill) cut the
// graceful shutdown short. It runs as its own goroutine because what it has
// to interrupt is a blocking wait on the grace timer: a handler inside the
// select loop could not see the signal until that wait finished anyway. The
// first signal only announces the stop; each later one SIGKILLs both process
// groups, which closes the done channels the graceful path is blocked on.
func (l *reloadLoop) escalateOnSecondSignal() {
	go func() {
		seen := 0
		for sig := range l.signals {
			seen++
			if seen == 1 {
				l.console.Note("stopping the dev servers; press Ctrl-C again to force it down")
				continue
			}
			l.console.Emit("WARN", "second "+sig.String()+": forcing the dev servers down")
			l.child.Force()
			l.vite.Force()
		}
	}()
}

// onChange rebuilds and restarts on a change batch. A build failure is
// reported and swallowed: the running process stays up, so a typo never takes
// the dashboard down and the next save retries.
func (l *reloadLoop) onChange(batch []string) (<-chan struct{}, bool) {
	if len(batch) == 0 {
		return nil, false
	}
	trigger := changeTrigger(batch)
	started := time.Now()

	// The build's own output is only interesting when it fails, so it goes to
	// the log and a tail buffer rather than the console: a successful reload
	// is one line, not forty.
	buildTee := l.console.QuietStream()
	if err := buildHostAgentTo(l.opts.root, l.opts.binaryPath, buildTee, buildTee); err != nil {
		l.console.ReloadFailed(trigger, joinTail(buildTee.Tail(devTailLines)))
		return nil, false
	}
	buildDur := time.Since(started)

	// The old process is torn down on purpose here, so its shutdown warnings
	// go to the log rather than the console: see SuppressAgentLog.
	l.console.SuppressAgentLog(true)
	next, err := restartChild(l.child)
	l.console.SuppressAgentLog(false)
	if err != nil {
		l.console.ReloadFailed(trigger, "restart failed: "+err.Error())
		return nil, false
	}
	l.crashes = 0
	l.console.ReloadOK(trigger, buildDur)
	return next, true
}

// onChildDeath applies the crash policy to an unsolicited host-agent exit and
// returns the channel to watch next: the restarted child's, or nil when the
// crash budget is spent. A stop that was asked for never reaches here,
// because restartChild replaces the channel before the loop looks at it again.
func (l *reloadLoop) onChildDeath() <-chan struct{} {
	l.crashes++
	if l.crashes > maxCrashes {
		l.console.Emit("ERROR", fmt.Sprintf(
			"host-agent has crashed on startup %d times in a row: auto-restart is off. Fix the error and save any watched file to try again.", l.crashes))
		return nil
	}
	l.console.Emit("WARN", "host-agent exited on its own; restarting in "+crashBackoff.String())
	time.Sleep(crashBackoff)
	next, err := l.child.Start()
	if err != nil {
		l.console.Emit("ERROR", "restart failed: "+err.Error())
		return nil
	}
	return next
}

// onViteDeath brings the dashboard's dev server back after it exits by
// itself. The API and every app container are unaffected when vite dies, so
// this is reported separately rather than as a dev-loop failure.
func (l *reloadLoop) onViteDeath(ctx context.Context) (<-chan struct{}, bool) {
	l.console.Emit("WARN", "dashboard dev server exited; restarting in "+crashBackoff.String())
	select {
	case <-ctx.Done():
		return nil, false
	case <-time.After(crashBackoff):
	}
	next, err := l.vite.Start()
	if err != nil {
		l.console.Emit("ERROR", "could not restart the dashboard dev server: "+err.Error())
		return nil, true
	}
	return next, true
}

// restartChild stops the current child and starts a fresh one from the binary
// on disk. The gap between them is the whole reload window.
func restartChild(c *restartableCmd) (<-chan struct{}, error) {
	c.Stop(gracefulStopTimeout)
	return c.Start()
}

// buildHostAgentTo compiles the host-agent into outPath. The build lands in a
// sibling temp file and is renamed into place, so a reader never sees a half
// written binary and a failed build cannot clobber the good one.
func buildHostAgentTo(root, outPath string, stdout, stderr io.Writer) error {
	tmp := outPath + ".building"
	cmd := exec.Command("go", "build", "-o", tmp, "./cmd/host-agent")
	cmd.Dir = filepath.Join(root, "services", "host-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, outPath)
}

// viteCommand builds the dashboard dev server command. It runs on the
// developer's machine, not in the runtime, because that is where the source
// and the node toolchain live.
//
// It is deliberately not an exec.CommandContext. The default context cancel is
// a bare Process.Kill of the direct child, which fires the moment the loop's
// context is canceled and races the ordered SIGTERM in restartableCmd.Stop:
// every Ctrl-C would become a forced kill that skips the group. restartableCmd
// owns this process's lifecycle, so the command carries no context of its own.
func viteCommand(root string, stdout, stderr io.Writer) (*exec.Cmd, error) {
	cmd := exec.Command("npm", "run", "dev", "--workspace=@bloud/host-agent-web")
	cmd.Dir = root
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd, nil
}

// hostAgentCommand builds one host-agent run. The dev switches it starts with
// are the point of the loop: the vite proxy for the dashboard, and the fast
// gate so the API comes back in seconds instead of after a full convergence
// pass.
func hostAgentCommand(opts hotReloadOptions, stdout, stderr io.Writer) (*exec.Cmd, error) {
	if _, err := os.Stat(opts.binaryPath); err != nil {
		return nil, fmt.Errorf("host-agent binary is missing (%s): %w", opts.binaryPath, err)
	}
	cmd := exec.Command(opts.binaryPath)
	cmd.Dir = opts.runDir
	cmd.Env = append(os.Environ(), envPairs(opts.env)...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd, nil
}

// envPairs flattens an env map into KEY=VALUE strings.
func envPairs(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
