// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// devVitePort is the port the dashboard's dev server listens on. It matches
// `vite dev --port 5173` in the web workspace's package.json. The two are
// kept in step here rather than discovered, because the host-agent has to be
// told where to proxy before vite has told anyone anything.
const devVitePort = 5173

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
}

// runHotReload keeps a freshly built host-agent running on top of a stack that
// is already up, with a vite dev server feeding the browser.
//
// The containers are never touched. That is the contract the whole feature is
// built on: a code change swaps the control-plane process, and the next
// reconciliation pass simply runs against the new binary. App containers keep
// running through every reload, so a reload costs seconds instead of a cold
// install, and nothing about a code edit can disturb a running app.
func runHotReload(ctx context.Context, opts hotReloadOptions, out io.Writer) int {
	if err := os.MkdirAll(opts.runDir, 0o755); err != nil {
		errorf("could not create the runtime dir: %v", err)
		return 1
	}

	fprintLog(out, "Building host-agent for linux/"+runtime.GOARCH)
	if err := buildHostAgentTo(opts.root, opts.binaryPath); err != nil {
		errorf("Build failed: %v", err)
		return 1
	}

	vite := newRestartableCmd("vite", func() (*exec.Cmd, error) {
		return viteCommand(ctx, opts.root)
	})
	viteDone, err := vite.Start()
	if err != nil {
		errorf("Could not start the vite dev server: %v", err)
		return 1
	}
	fprintLog(out, fmt.Sprintf(
		"vite dev server up: the dashboard hot-reloads from it, proxied through the host-agent so the origin and the OIDC round trip stay real (dev server: http://localhost:%d).",
		devVitePort))

	child := newRestartableCmd("host-agent", func() (*exec.Cmd, error) {
		return hostAgentCommand(opts)
	})
	childDone, err := child.Start()
	if err != nil {
		errorf("Could not start host-agent: %v", err)
		_ = vite.Stop(gracefulStopTimeout)
		return 1
	}
	fprintLog(out, "host-agent up. Watching services/host-agent and apps; Ctrl-C to stop.")

	changes := make(chan []string, 8)
	go watchBackendSources(ctx, opts.root, changes)

	loop := &reloadLoop{
		out:     out,
		opts:    opts,
		child:   child,
		vite:    vite,
		changes: changes,
	}
	return loop.run(ctx, childDone, viteDone)
}

// reloadLoop is the watch loop's event handling, split out from bring-up so
// each handler stays small enough to read on its own.
type reloadLoop struct {
	out     io.Writer
	opts    hotReloadOptions
	child   *restartableCmd
	vite    *restartableCmd
	changes <-chan []string

	crashes int
}

// run consumes events until the context is cancelled. The two done channels
// are passed by pointer because each handler replaces them with the next
// child's channel.
func (l *reloadLoop) run(ctx context.Context, childDone, viteDone <-chan struct{}) int {
	for {
		select {
		case <-ctx.Done():
			fprintLog(l.out, "Stopping the dev loop")
			_ = l.child.Stop(gracefulStopTimeout)
			_ = l.vite.Stop(gracefulStopTimeout)
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

// onChange rebuilds and restarts on a change batch. A build failure is
// reported and swallowed: the running process stays up, so a typo never takes
// the dashboard down and the next save retries.
func (l *reloadLoop) onChange(batch []string) (<-chan struct{}, bool) {
	if len(batch) == 0 {
		return nil, false
	}
	fprintLog(l.out, describeChangeBatch(batch))
	started := time.Now()
	if err := buildHostAgentTo(l.opts.root, l.opts.binaryPath); err != nil {
		errorf("build failed, keeping the running host-agent: %v", err)
		return nil, false
	}
	fprintLog(l.out, "built in "+time.Since(started).Round(time.Millisecond).String())

	next, err := restartChild(l.child)
	if err != nil {
		errorf("restart failed: %v", err)
		return nil, false
	}
	l.crashes = 0
	fprintLog(l.out, "host-agent reloaded. Containers were left alone: the next reconciliation runs against the new code.")
	return next, true
}

// onChildDeath applies the crash policy to an unsolicited host-agent exit and
// returns the channel to watch next: the restarted child's, or nil when the
// crash budget is spent. A stop that was asked for never reaches here,
// because restartChild replaces the channel before the loop looks at it again.
func (l *reloadLoop) onChildDeath() <-chan struct{} {
	l.crashes++
	if l.crashes > maxCrashes {
		errorf("host-agent has crashed on startup %d times in a row; auto-restart is off. Fix the error above and save any watched file to try again.", l.crashes)
		return nil
	}
	fprintLog(l.out, "host-agent exited on its own; restarting in "+crashBackoff.String())
	time.Sleep(crashBackoff)
	next, err := l.child.Start()
	if err != nil {
		errorf("restart failed: %v", err)
		return nil
	}
	return next
}

// onViteDeath brings the dashboard's dev server back after it exits by
// itself. The API and every app container are unaffected when vite dies, so
// this is reported separately rather than as a dev-loop failure.
func (l *reloadLoop) onViteDeath(ctx context.Context) (<-chan struct{}, bool) {
	fprintLog(l.out, "vite dev server exited; restarting in "+crashBackoff.String())
	select {
	case <-ctx.Done():
		return nil, false
	case <-time.After(crashBackoff):
	}
	next, err := l.vite.Start()
	if err != nil {
		errorf("could not restart vite: %v", err)
		return nil, true
	}
	return next, true
}

// restartChild stops the current child and starts a fresh one from the binary
// on disk. The gap between them is the whole reload window.
func restartChild(c *restartableCmd) (<-chan struct{}, error) {
	if err := c.Stop(gracefulStopTimeout); err != nil {
		return nil, err
	}
	return c.Start()
}

// buildHostAgentTo compiles the host-agent into outPath. The build lands in a
// sibling temp file and is renamed into place, so a reader never sees a half
// written binary and a failed build cannot clobber the good one.
func buildHostAgentTo(root, outPath string) error {
	tmp := outPath + ".building"
	cmd := exec.Command("go", "build", "-o", tmp, "./cmd/host-agent")
	cmd.Dir = filepath.Join(root, "services", "host-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, outPath)
}

// viteCommand builds the dashboard dev server command. It runs on the
// developer's machine, not in the runtime, because that is where the source
// and the node toolchain live.
func viteCommand(ctx context.Context, root string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "npm", "run", "dev", "--workspace=@bloud/host-agent-web")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd, nil
}

// hostAgentCommand builds one host-agent run. The dev switches it starts with
// are the point of the loop: the vite proxy for the dashboard, and the fast
// gate so the API comes back in seconds instead of after a full convergence
// pass.
func hostAgentCommand(opts hotReloadOptions) (*exec.Cmd, error) {
	if _, err := os.Stat(opts.binaryPath); err != nil {
		return nil, fmt.Errorf("host-agent binary is missing (%s): %w", opts.binaryPath, err)
	}
	cmd := exec.Command(opts.binaryPath)
	cmd.Dir = opts.runDir
	cmd.Env = append(os.Environ(), envPairs(opts.env)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
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
