// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// backendSourceRoots are the repo-relative trees whose contents end up inside
// the host-agent binary. Everything outside them can change without the backend
// needing a restart: the frontend has its own dev server, docs and validation
// config are not compiled in.
var backendSourceRoots = []string{"services/host-agent", "apps"}

// watchSkipDirs never carry backend source. node_modules and .svelte-kit belong
// to the frontend toolchain and are enormous; walking them every poll interval
// would make the watcher the slowest thing in the loop.
var watchSkipDirs = map[string]bool{
	"node_modules": true,
	".svelte-kit":  true,
	".git":         true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
}

// isBackendSourceFile reports whether a path is something whose edit changes
// what the host-agent runs.
//
// Test files are excluded on purpose: they are not compiled into the binary, so
// restarting the whole control plane because a *_test.go was saved would be
// pure noise. App metadata is included because the catalog is read from disk at
// startup (invariant 5), so a metadata edit only takes effect in a new
// process.
func isBackendSourceFile(path string) bool {
	switch {
	case strings.HasSuffix(path, ".go"):
		return !strings.HasSuffix(path, "_test.go")
	case strings.HasSuffix(path, "metadata.yaml"), strings.HasSuffix(path, "metadata.yml"):
		return true
	default:
		return false
	}
}

// fileStamp is what a watcher remembers about one file. Size is part of it
// because a filesystem with a coarse mtime clock can report the same second
// before and after a write; the size catches the ones the timestamp misses.
type fileStamp struct {
	modTime time.Time
	size    int64
}

// sourceSnapshot maps an absolute path to its last-seen stamp.
type sourceSnapshot map[string]fileStamp

// takeSnapshot walks root and records every file the include predicate accepts.
func takeSnapshot(root string, include func(string) bool) (sourceSnapshot, error) {
	snap := sourceSnapshot{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A file that vanished mid-walk is a normal race in an editor
			// loop; a directory we cannot read is worth reporting.
			if d != nil && d.IsDir() {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if watchSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !include(path) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		snap[path] = fileStamp{modTime: info.ModTime(), size: info.Size()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// takeSourceSnapshot snapshots every configured backend source root under root.
func takeSourceSnapshot(root string) (sourceSnapshot, error) {
	combined := sourceSnapshot{}
	for _, rel := range backendSourceRoots {
		snap, err := takeSnapshot(filepath.Join(root, rel), isBackendSourceFile)
		if err != nil {
			return nil, err
		}
		for path, stamp := range snap {
			combined[path] = stamp
		}
	}
	return combined, nil
}

// diffSnapshots returns the paths that were added, changed, or removed between
// two snapshots, sorted so the output is stable.
func diffSnapshots(prev, next sourceSnapshot) []string {
	seen := map[string]bool{}
	var changed []string
	for path, stamp := range next {
		old, existed := prev[path]
		if !existed || old.modTime != stamp.modTime || old.size != stamp.size {
			changed = append(changed, path)
			seen[path] = true
		}
	}
	for path := range prev {
		if _, still := next[path]; !still && !seen[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

// debounceWindow is how long the watcher waits after the first change before
// reporting a batch. An editor save frequently lands as two writes, and a
// refactor touches several files at once; without coalescing each of those
// would trigger a separate rebuild and restart.
const debounceWindow = 300 * time.Millisecond

// pollInterval is how often the trees are re-walked. Polling rather than
// inotify keeps the CLI free of a new dependency and behaves the same on every
// filesystem, including network mounts where inotify is unreliable.
const pollInterval = 500 * time.Millisecond

// watchBackendSources walks the backend source trees and sends debounced change
// batches to out. It returns when ctx is cancelled.
//
// The batch is delivered once the trees have been quiet for debounceWindow, so
// a multi-file save is one rebuild.
func watchBackendSources(ctx context.Context, root string, out chan<- []string) {
	watchTrees(ctx, root, pollInterval, debounceWindow, out)
}

// watchTrees is the polling core, split out so the interval and debounce can be
// driven by a test without waiting on wall-clock defaults.
func watchTrees(ctx context.Context, root string, poll, debounce time.Duration, out chan<- []string) {
	prev, err := takeSourceSnapshot(root)
	if err != nil {
		fprintLog(os.Stderr, "watcher could not read the source trees: "+err.Error())
		return
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	var pending []string
	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return

		case <-ticker.C:
			next, snapErr := takeSourceSnapshot(root)
			if snapErr != nil {
				// Transient: a tree being rewritten under us. Keep the last
				// good snapshot rather than treating the failure as "all
				// files removed", which would trigger a spurious rebuild.
				continue
			}
			if batch := diffSnapshots(prev, next); len(batch) > 0 {
				prev = next
				pending = append(pending, batch...)
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(debounce)
				timerC = timer.C
			}

		case <-timerC:
			if timer != nil {
				timer.Stop()
			}
			timerC = nil
			batch := pending
			pending = nil
			select {
			case out <- batch:
			case <-ctx.Done():
				return
			}
		}
	}
}

// restartableCmd owns one child process that can be stopped and started again.
// The dev loop needs exactly this: the host-agent is restarted on every code
// change while everything it manages stays put.
type restartableCmd struct {
	name   string
	launch func() (*exec.Cmd, error)

	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
}

// newRestartableCmd builds a restartable child. launch is called for every
// start, so the caller controls which binary and environment each run gets.
func newRestartableCmd(name string, launch func() (*exec.Cmd, error)) *restartableCmd {
	return &restartableCmd{name: name, launch: launch}
}

// Start launches the child and reaps it in the background. When the child exits
// the returned channel is closed, so the caller can tell a restart it asked
// for from a crash it did not.
func (c *restartableCmd) Start() (<-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return nil, nil
	}
	cmd, err := c.launch()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c.cmd = cmd
	done := make(chan struct{})
	c.done = done
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return done, nil
}

// Running reports whether a child is currently up.
func (c *restartableCmd) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil
}

// Stop sends SIGTERM and waits up to grace for a clean exit, then SIGKILL.
// Grace matters: host-agent's shutdown path stops its orchestrator loop, and
// killing it earlier would turn every reload into a forced kill.
func (c *restartableCmd) Stop(grace time.Duration) error {
	c.mu.Lock()
	cmd := c.cmd
	done := c.done
	c.stopping = true
	c.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)

	if done != nil {
		select {
		case <-done:
		case <-time.After(grace):
			_ = cmd.Process.Kill()
			<-done
		}
	}

	c.mu.Lock()
	c.cmd = nil
	c.done = nil
	c.stopping = false
	c.mu.Unlock()
	return nil
}

// describeChangeBatch names what changed, compactly. A raw list of absolute
// paths is unreadable; the developer only needs to know which area moved.
func describeChangeBatch(batch []string) string {
	if len(batch) == 1 {
		return "change detected: " + shortSourcePath(batch[0])
	}
	areas := map[string]bool{}
	for _, p := range batch {
		areas[shortSourcePath(p)] = true
	}
	names := make([]string, 0, len(areas))
	for name := range areas {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 4 {
		names = append(names[:3], "...")
	}
	return "changes detected in " + strings.Join(names, ", ")
}

// shortSourcePath trims a long absolute path down to the part a developer
// recognises, preferring the segment after services/ or the app name.
func shortSourcePath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		if part == "services" && i+1 < len(parts) {
			return strings.Join(parts[i:], "/")
		}
	}
	if len(parts) > 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return path
}
