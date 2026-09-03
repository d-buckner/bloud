# Plan: Fix the Native (No-VM) Backend + Apt-Based Setup Tooling

**Status:** Proposed — not yet implemented.
**Last updated:** 2026-09-02

---

## Summary

`main` already merged a third `backend.Backend` implementation — `NativeBackend`
(`cli/backend/native.go`, from #60/#61) — that runs `./bloud dev` directly on the current
machine instead of provisioning a Lima or QEMU VM, plus an interactive backend-preference
system (`cli/preferences.go`, `.bloud/preferences.yaml`). **The architectural piece of
this plan is done and does not need to be redesigned.** This revision replaces an earlier
draft that proposed building that scaffolding from scratch.

What's left is narrower: the native backend is wired up but **broken for the interactive
CLI path**, and the dependency-auto-install tooling that was the other half of the
original ask was never added. Concretely:

1. **`executor.LocalExecutor` doesn't shell-wrap commands** — every non-trivial `RunSpec`
   in `cli/dev.go` is a shell script (`;`, `2>/dev/null`, `$(...)`, pipes), but
   `LocalExecutor.buildLocalCommand` passes `spec.Command` straight to `exec.CommandContext`
   as a literal argv0. Confirmed by reproduction (see §1): a PATH lookup on the exact
   string `cmdStop` sends fails immediately. `BLOUD_BACKEND=native ./bloud dev` fails on
   its first non-trivial step today. It only "works" in CI because
   `cli/e2e_lifecycle.go`/`e2e_app.go` have their own separate, correctly shell-wrapped
   `remoteRun`/`remoteCommand` plumbing that bypasses `Host().Executor()` entirely — the
   interactive path was never actually exercised.
2. **`LocalExecutor.CopyTo`/`CopyFrom` only copy single files**, but `cmdDev` copies a
   whole directory (`web/build`) through the same call.
3. **No apt-based dependency auto-install exists anywhere in `cli/`** (verified: zero
   `apt-get` references in the tree). `cmdSetup()` only checks `go`/`node`/`podman` via a
   PATH scan and reports pass/fail — this was the explicit, primary ask and is still
   entirely unaddressed.
4. **`cmdReset` unconditionally wipes `$HOME/.local/share/bloud`** — safe cleanup on a
   disposable Lima/QEMU guest, but now a live data-loss footgun on native, where `$HOME`
   is the contributor's real home and that path is host-agent's real default
   `BLOUD_DATA_DIR` fallback.
5. **README.md's "Local development" section is stale** — still says only "Development
   runs inside a Lima VM," with no mention of `native`/QEMU. `AGENTS.md` documents `native`
   correctly and doesn't need changes.

---

## 1. Reproducing the `LocalExecutor` bug

`cli/executor/local.go`:

```go
func buildLocalCommand(ctx context.Context, spec RunSpec) *exec.Cmd {
	name, args := spec.Command, spec.Args
	if spec.AsRoot {
		name, args = "sudo", append([]string{spec.Command}, spec.Args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	...
}
```

`cli/dev.go`'s `cmdStop` sends:

```go
Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop apps-*.service 2>/dev/null; true`,
```

with no `Args`. `exec.CommandContext` resolves a bare name (no `/`) via `exec.LookPath`,
which searches `PATH` for a file with that *exact* name — it does not invoke a shell or
split on whitespace. Simulating that resolution:

```bash
$ cmd="pkill -f 'host-agent\$' 2>/dev/null; systemctl --user stop apps-*.service 2>/dev/null; true"
$ command -v "$cmd"; echo "exit=$?"
exit=1
```

No such "executable" exists, so every `RunStream`/`Run` call through a `LocalExecutor`
with a shell-syntax `Command` fails immediately with "executable file not found in
$PATH." This hits `cmdStop`, `cmdReset`, `cmdLogs`, `cmdServices`, `cmdShell`, and most of
`cmdDev`'s steps (the container-cleanup one-liner, `mkdir -p`, `chmod 755`, the
`fuser`/`pkill` port-kill step, and the final `unset DATABASE_URL; exec ./host-agent` run
command).

---

## 2. Fix: shell-wrap `LocalExecutor.Run`/`RunStream`

`cli/executor/ssh.go` already has exactly the right primitive: `BuildRemoteScript(spec)`
renders a `RunSpec` into a bash script string, and every other transport (`limactl`,
real `ssh`) runs it via `bash -c <script>`. Do the same for local:

```go
func buildLocalCommand(ctx context.Context, spec RunSpec) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bash", "-c", BuildRemoteScript(spec))
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), envSlice(spec.Env)...)
	}
	cmd.Dir = spec.Dir
	cmd.Stdin = spec.Stdin
	return cmd
}
```

This also makes the separate `spec.Env`/`spec.Dir`/`spec.AsRoot` handling in
`buildLocalCommand` redundant with what `BuildRemoteScript` already renders into the
script (it exports env vars, `cd`s, and prefixes `sudo` inside the script string) — drop
the now-duplicated `cmd.Dir`/manual env handling once `BuildRemoteScript` is doing that
work, so env/dir/sudo semantics stay identical across all three transports instead of
being handled twice, inconsistently. Verify against `cli/executor/ssh_test.go`'s existing
coverage of `BuildRemoteScript` for the exact rendering rules.

**Test**: extend `cli/executor/executor_test.go` (or add `local_test.go`) with a
regression case using the literal `cmdStop` script (`;`, `2>/dev/null`) and confirm it now
runs instead of failing on PATH lookup.

---

## 3. Fix: directory-recursive `CopyTo`/`CopyFrom`

Current implementation is `os.Open`/`os.Create`/`io.Copy` — single files only. Replace
with `cp -a`, matching the semantics `SSHExecutor.CopyTo` already documents (directories
copied recursively, matching `limactl copy -r`/rsync-with-trailing-slash behavior):

```go
func (e *LocalExecutor) CopyTo(ctx context.Context, from, to string) error {
	return exec.CommandContext(ctx, "cp", "-a", from, to).Run()
}
func (e *LocalExecutor) CopyFrom(ctx context.Context, from, to string) error {
	return exec.CommandContext(ctx, "cp", "-a", from, to).Run()
}
```

Confirmed against `cmdDev`'s two call shapes: `CopyTo(binaryPath, dirs.HostAgentDir+"/host-agent")`
(file→file) and `CopyTo(webBuildDir, dirs.HostAgentDir+"/web/build")` (dir→new-dir, called
right after `mkdir -p .../web`, so `cp -a src dst` where `dst` doesn't yet exist correctly
places `src`'s contents at `dst`). The unused `copyFile` helper can be deleted once nothing
calls it — check `envSlice` still has other callers before removing that too, or clean up
whatever becomes dead code.

**Test**: extend the executor test file with a directory-copy case containing nested
files/subdirectories.

---

## 4. Fix: `cmdReset`'s `$HOME` footgun for `native`

`cli/dev.go`:

```go
Command: fmt.Sprintf(`
set -e
podman unshare rm -rf "$HOME/.local/share/bloud"
podman unshare rm -rf %s
rm -f %s/bloud.db
`, dirs.DataDir, dirs.DataDir),
```

Exclude the `$HOME/.local/share/bloud` line when the backend is `native` — that path is
host-agent's real default `BLOUD_DATA_DIR` fallback on a contributor's actual machine, not
a disposable guest home. `cmdReset` already receives `name` from `devBackend()`, so gate
on `name == "native"`:

```go
wipe := fmt.Sprintf("set -e\npodman unshare rm -rf %s\nrm -f %s/bloud.db\n", dirs.DataDir, dirs.DataDir)
if name != "native" {
	wipe = "set -e\npodman unshare rm -rf \"$HOME/.local/share/bloud\"\n" + wipe[len("set -e\n"):]
}
```

(exact string assembly is a style choice — the important part is the conditional, not the
formatting).

**Test**: not easily unit-testable (shells a script); cover in the manual checklist (§7).

---

## 5. Add: apt-based dependency auto-install in `cmdSetup()`

This is the piece that was never built. Current `cmdSetup()` (`cli/setup.go`) only checks
`go`, `node`, `podman` (+ `qemu-system-x86_64` or `limactl` depending on backend) via
`checkCommand` (a PATH scan) and reports pass/fail.

Add, gated on `bkName == "native"` and running on an apt host
(`runtime.GOOS == "linux" && checkCommand("apt-get")`):

1. After the prereq loop, if `!allGood` and this condition holds, map the missing
   `label`s back to apt package names (`go`→`golang-go`, `Node.js`→`nodejs npm`,
   `Podman`→`podman`) and run:

   ```bash
   sudo apt-get update -qq && sudo apt-get install -y -qq <missing packages...>
   ```

   with `Stdin`/`Stdout`/`Stderr` wired to the terminal (so `sudo` can prompt), then
   re-run the prereq checks before deciding `allGood`. Every other backend/OS combination
   keeps today's check-and-report-only behavior unchanged.
2. After prereqs pass on `native`, add a small `configureNativePodman()` step (this
   overlaps with what `NativeBackend.Create()` already does for linger/`podman.socket`,
   so keep it additive, not duplicated — `Create()` already runs on every `./bloud dev`,
   so `setup` only needs to add the one thing `Create()` doesn't check):
   - Verify `/etc/subuid`/`/etc/subgid` have an entry for the current user (common gap on
     accounts created before podman was installed); if missing, run
     `sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $(id -un)`.
   - Print a note that `loginctl enable-linger`/socket changes (handled by
     `NativeBackend.Create` on the next `./bloud dev`) sometimes need a fresh login
     session to take effect.
3. Do **not** write a `cgroup_manager = "cgroupfs"` override to
   `~/.config/containers/containers.conf`. That override (present in `dev/lima.yaml`'s
   guest provisioning) works around Lima-guest-specific cgroup delegation quirks; a real
   Debian 13 host has proper cgroup v2 delegation and should use Podman's default
   `systemd` cgroup manager. Silently rewriting a contributor's real container config
   would be a surprising side effect on their actual machine — leave this as a manual
   troubleshooting note in README instead, not an automatic step.

**Test**: `cli/setup_test.go` (new) covering the missing-package→apt-package-name mapping
as a pure function; do not unit-test the actual `sudo apt-get`/`usermod` invocations
(covered by the manual checklist instead).

---

## 6. Docs

- **`README.md`** "Local development" section: currently says only "Development runs
  inside a Lima VM." Update to mention all three backends (matching what `AGENTS.md`
  already documents accurately) and add the `native` quick-start:

  ```bash
  BLOUD_BACKEND=native ./bloud setup   # installs podman/go/node via apt if missing (sudo)
  BLOUD_BACKEND=native ./bloud dev
  ```

  Include the manual `cgroup_manager` fallback recipe as a troubleshooting note (§5.3).
- `AGENTS.md` already documents `native` correctly — no changes needed there.

---

## Build order

1. `cli/executor/local.go`: shell-wrap fix (§2) + directory-copy fix (§3) — these are the
   blocking correctness bugs; nothing else matters until `./bloud dev` can actually run on
   `native`.
2. `cli/dev.go`: `cmdReset` footgun fix (§4) — small, independent.
3. `cli/setup.go`: apt auto-install + subuid check (§5) — independent of 1–2, can be done
   in parallel.
4. `README.md` update (§6) — last, once behavior is settled.

---

## 7. Verification

**Unit tests**: extend `cli/executor/executor_test.go` (shell-syntax command regression,
directory-copy regression per §2/§3); new `cli/setup_test.go` (apt package-name mapping
per §5). Run `go test ./...` from `cli/` alongside existing `lima_test.go`/`qemu_test.go`/
`ssh_test.go` to confirm no regressions.

**Manual end-to-end checklist** (needs a real or throwaway Debian 13 box — this can't be
verified from a Mac dev machine):

1. Fresh Debian 13, nothing installed. `BLOUD_BACKEND=native ./bloud setup` — confirm apt
   auto-installs podman/golang-go/nodejs/npm, fixes subuid/subgid, builds `./bloud`.
2. Re-run `./bloud setup` — confirm idempotent.
3. `BLOUD_BACKEND=native ./bloud dev` — confirm build, deploy to
   `/var/tmp/bloud-native-runtime/host-agent`, `curl localhost:3000/api/health` succeeds.
   (This is the step that fails outright today — the primary thing this plan fixes.)
4. `./bloud install jellyfin` — confirm it starts under Podman's default systemd cgroup
   manager.
5. `./bloud status`/`services`/`logs` — confirm they work (currently broken per §1).
6. `./bloud stop` then `./bloud dev` again — clean restart (currently broken per §1).
7. `./bloud reset` (with a decoy file first at `~/.local/share/bloud`) — confirm data
   wiped but that real path is untouched (validates §4's fix).
8. `./bloud destroy` — confirm `/var/tmp/bloud-native-runtime` gone, project checkout
   untouched.
