# CI flakiness reduction

> Status: draft

This plan records what the CI data actually shows, names the root causes behind
the recurring red runs, and orders the work to fix them. It exists because a
green main is the precondition for every other change: when one push in five
comes back red for reasons unrelated to the code, reviewers stop trusting the
result and start rerunning until it passes.

The analysis covers every workflow run in the repository's history
(`d-buckner/bloud`, 1,990 runs, 2026-01-13 through 2026-10-02), pulled through
the GitHub Actions API. Failure reasons come from the failed job logs and
annotations, not from the run conclusions alone.

## How flakiness is told apart from a real failure

A red job is not automatically a flake. The classification used here:

- **Flaky**: the same assertion fails across unrelated commits and branches
  with no matching code change; or the same commit both passed and failed; or
  the failure is a timeout/readiness race whose budget is tuned for an idle
  machine.
- **Deterministic**: the failure names the change (a lint gate, a stale
  generated file, a test that pins new behavior, a genuinely broken app).

This distinction is the whole point. The data shows that most red CI is
deterministic, and a smaller but steady stream is genuine flakiness. Fixing the
deterministic half is a process change; fixing the flaky half is test and
readiness work.

## Fix it in the platform, not in the app

A flake that recurs across apps is a harness bug, and the fix belongs in the
shared layer the apps already use, not in each app's spec. Two examples from
this work make the rule concrete:

- The forward-auth rungs live in `describeApp` (`e2e/lib/app-suite.ts`) and take
  three per-app values (`label`, `title`, `appShell`). The fresh-context fix is
  in the rung, so every forward-auth app gets it, and a new one is covered by
  passing its values rather than by copying two `test(...)` blocks.
- The LDAP readiness barrier lives in `waitAppRunning`
  (`services/host-agent/internal/e2e/e2e_test.go`) and derives the app's
  strategy from catalog metadata. A new LDAP app is covered the moment it
  declares `sso.strategy: ldap`.

The contrast is the test for any such fix: a change in one app's spec fixes one
app; a change in the harness fixes the class. When a rung is identical except
for app-specific data, the rung is the framework's and the data is the spec's.

## Headline numbers

Runs on `main` since 2026-09-01 (the period covered by the current workflow
set). The failure count is per run, and a run can fail more than one job.

| Workflow | Runs | Failures | Failure rate |
|---|---|---|---|
| CI (fast tier) | 105 | 7 | 6.7% |
| E2E (Jellyfin lifecycle) | 105 | 1 | 1.0% |
| E2E (per-app matrix) | 105 | 19 | 18.1% |
| Integration | 25 | 7 | 28.0% |
| Release (.deb) | 54 | 7 | 13.0% |
| Generated docs | 23 | 3 | 13.0% |

Per-app E2E jobs since 2026-08-15 (job attempts, not runs). The `main` column
is the one that matters: a feature branch is expected to go red while it is
being written.

| Job | All branches | On `main` |
|---|---|---|
| `e2e (homeassistant)` | 32/286 | 2/92 |
| `jellyfin-lifecycle` | 29/333 | 1/105 |
| `e2e (jellyfin)` | 25/324 | 2/101 |
| `e2e (affine)` | 14/324 | 5/101 |
| `e2e (navidrome)` | 12/325 | 2/101 |
| `e2e (prowlarr)` | 9/133 | 4/40 |
| `e2e (paperless-ngx)` | 8/181 | 2/57 |
| `e2e (sonarr)` | 7/133 | 0/40 |
| `e2e (radarr)` | 6/133 | 2/40 |
| `e2e (calino)` | 4/30 | 1/5 |
| `e2e (radicale)` | 4/33 | 0/6 |
| the rest | 35/700 | 5/300 |

Across every E2E job attempt since 2026-08-15: 185 failures in 3,185 attempts,
5.8%. A large share of those are concentrated in the 2026-09-01 to 2026-09-05
window, when the native backend was first wired up and nearly every push to
the migration branch failed. The flakiness that survives today is narrower and
is listed below.

Only 23 of 1,990 runs were ever rerun (`run_attempt > 1`), and only two commits
in the whole history both passed and failed on the same workflow. Automatic
retries are rare here, so a same-SHA pass/fail pair is a conservative lower
bound on flake rate, not a good detector. The useful detector is recurrence of
the same assertion across unrelated commits.

## Confirmed root causes

### F1. The forward-auth gate rung depends on IdP session state

**Symptom.** `expectForwardAuthPrompt` fails with
`forward-auth did not prompt: the popup is at http://<app>.localhost:8080/`
or at `http://localhost:8080/auth/callback?code=...`.

**Occurrences.** 18 failed jobs across 17 runs, 2026-09-25 to 2026-10-02,
hitting prowlarr, sonarr, radarr, qbittorrent, navidrome, and calino. It is the
single most frequent current E2E flake, and it appears on `main`.

**Mechanism.** `e2e/lib/app-suite.ts` signs the shared browser context into
Authentik once (`ensureSignedIn`). Every forward-auth spec then opens the app
popup from that same signed-in context and asserts the popup lands on the
Authentik prompt (`e2e/lib/forwardAuth.ts`). That assertion assumes a browser
that carries a valid Authentik session is still asked to authenticate by the
embedded outpost. It is not guaranteed: when the outpost accepts the existing
session it redirects straight back to the app, and the popup lands on the app
origin with no prompt. The two failure URLs are the two faces of the same
problem: the app origin means "authorized without a prompt", and Bloud's own
`/auth/callback` means the popup ended up in the wrong flow entirely.

**Fix (landed in the platform layer).** The gate and sign-in rungs moved into
the shared suite: `describeApp('prowlarr', body, { forwardAuth: { label, title,
appShell } })` in `e2e/lib/app-suite.ts` registers both rungs, and the gate rung
opens its own `browser.newContext()` and goes straight to the app origin. A
context with no Authentik session deterministically receives the identification
prompt. Each spec now supplies only its three app-specific values; none of them
repeats the rung logic, so the next forward-auth app gets the fix for free. The
AFFiNE spec already used fresh contexts for its OIDC rungs for the same reason.

**Effort.** Small. **Impact.** Removes the largest current E2E flake.

### F2. The integration tier races the LDAP outpost after app installs

**Symptom.** A cluster of failures in one run:
`ldap_result: Can't contact LDAP server (-1)` for
`TestLDAPAuth_ServiceAccountCanBind` and `TestLDAPAuth_AuthentikAdminCanBind`;
`Jellyfin LDAP login as admin failed: status 401`; and every authenticated DAV
request in the Radicale tests returning 500 with
`A server error occurred. Please contact the administrator.` The podman
listing at the end of the same job shows `apps-authentik-ldap` up and bound to
`:3389`.

**Occurrences.** `TestRadicaleWritesACalendar` failed in 26 integration jobs on
2026-10-01 and 2026-10-02. The multi-test cluster appears in the runs where the
outpost was unreachable. 2026-10-02 also shows `TestJellyfinLDAPLogin`,
`TestCrashRecoveryViaReconcile` and the two LDAP bind tests failing together.

**Mechanism.** The integration suite installs several apps through the real
orchestrator (`TestAffineInstallViaAPI`, `TestJellyfinInstallViaAPI`,
`TestPaperlessNgxInstallViaAPI`, `TestRadicaleInstallViaAPI`,
`TestVaultwardenInstallViaAPI`). Each install re-provisions Authentik and can
restart the LDAP outpost container. The LDAP-dependent tests that follow call
`waitAppRunning` on their own app, but nothing waits for the outpost itself to
be reachable again. When a test runs in the window between "the outpost was
restarted" and "the outpost accepts binds", every LDAP consumer in the suite
fails at once, and the failure looks like a broken app rather than a readiness
gap. Radicale is the most exposed because its LDAP auth turns an unreachable
outpost into an application 500.

**Fix (landed in the platform layer).** The readiness barrier lives in the
harness, not in the tests: `waitAppRunning` (in
`services/host-agent/internal/e2e/e2e_test.go`) reads
`GET /api/apps/{name}/metadata`, sees whether the catalog app declares the
`ldap` SSO strategy, and, if it does, waits for a real service-account bind on
`ldap://localhost:3389` before returning. The strategy comes from the metadata,
so a new LDAP app is covered the moment it declares `sso.strategy: ldap`,
without a per-test or per-app list to maintain. A real bind, not a TCP dial:
the port is published before the outpost can search. The LDAP-only tests in
`ldap_auth_test.go` call the same helper directly, since they do not go through
`waitAppRunning`.

**Effort.** Small. **Impact.** Collapses the multi-test integration cascades and
most of the current Radicale red.

### F3. Radicale's own gate is red on main (deterministic)

**Symptom.** After F2 is set aside, `TestRadicaleWritesACalendar` still fails
on `main`: first because a created calendar was absent from the parent
listing, and, at `8071c37`, because authenticated requests return 500.

**Occurrences.** 26 integration jobs, 2026-10-01 and 2026-10-02, on `main` and
on feature branches.

**Mechanism.** Two layers. The listing assertion was a test bug: a `PROPFIND`
without a `Depth` header asks for the collection alone (Radicale reads an
absent header as Depth 0), so the child calendar could never appear. `8071c37`
fixed that by sending `Depth: 1`. The 500s are F2: Radicale turns an
unreachable LDAP outpost into a 500, so the newest app's gate is red until the
outpost readiness barrier exists.

**Fix.** F2, plus keep `8071c37`. Confirm with one full integration run on
`main` after the barrier lands.

**Effort.** Included in F2. **Impact.** Unblocks the integration tier, which is
currently 28% red on `main`.

### F4. The self-heal test asserts the node is running as soon as the container is

**Symptom.** `--- FAIL: TestSelfHeal_TimerRepairsDriftWithoutAnIntent`,
`expected: "RUNNING" actual: "POSTSTART_CONFIG"`.

**Occurrences.** 3 fast-tier jobs, 2026-10-01 and 2026-10-02, under `-race`.

**Mechanism.** `selfheal_test.go:346` polls `f.rt.isRunning(healContainer)` and
breaks as soon as the fake runtime reports the container running, then asserts
`healNode(...).ActualStatus == graph.StatusRunning` on line 356. The container
is "running" while the orchestrator is still in `POSTSTART_CONFIG`; the node is
only promoted to `RUNNING` after that phase completes. The test samples the
graph in the window between the two, which widens under CI load.

**Fix.** Poll on the status under test, not on the runtime: wait until
`healNode(...).ActualStatus == graph.StatusRunning` (or a task-local
`waitUntil`), then assert. The container-running check can stay as a separate
assertion.

**Effort.** Small. **Impact.** Removes a recurring fast-tier race.

### F5. Timing budgets are too tight for a loaded runner

**Symptom A.** `--- FAIL: TestRestartableCmdStopKillsWholeTree` and
`TestRestartableCmdForceKillsWholeTree` with
`expected the grandchild (pid ...) to hold .../tree.lock`.
`startTree` waits 5 s for the grandchild to acquire its flock
(`cli/dev_process_test.go`).

**Symptom B.** `--- FAIL: TestTimeout_AppliesARealPerRequestDeadline` with
`the per-request deadline must actually cancel the request (server never saw a
client hang up)`. The handler gets a 2 s window to observe the cancellation of
a request whose deadline is 50 ms (`pkg/appclient/wait_budget_test.go`).

**Occurrences.** Restartable: 2 jobs on 2026-09-29. Per-request deadline:
3 jobs on 2026-09-26.

**Mechanism.** Both are genuine races with a fixed observe window. Neither is
a product bug: the assertions hold on an idle machine and lose the race on a
contended four-core runner, which is where the whole fast tier runs its checks
concurrently (`scripts/checks.mjs`).

**Fix.** Make the observation windows explicit and generous (for example 15 to
30 s), and poll rather than sample once. The assertions then fail only on a
real regression. A slow test is cheaper than a flaky one.

**Effort.** Small. **Impact.** Removes the remaining Go-test flakes.

### F6. The dev-loop takeover test races process identity after `Start`

**Symptom.** `--- FAIL: TestTakeoverPreviousDevLoopEscalatesToKill`,
`dev_stale_test.go:223: pid ... still alive`.

**Occurrences.** 2 fast-tier jobs, 2026-10-01 and 2026-10-02.

**Mechanism.** `writeFakeDevLoop` starts the fake loop and returns; the test
then calls `takeoverPreviousDevLoop`, which makes a single `isBloudDevProcess`
check against `/proc/<pid>/cmdline`. If the identity read happens before the
kernel has published the exec'd command line, the check answers "not ours", no
signal is sent, and `waitForGone` times out 15 s later. The same race was
already fixed in `TestIsBloudDevProcess` by polling for recognition
(`cli/dev_stale_test.go`), but the takeover path still samples once.

**Fix.** Either have `writeFakeDevLoop` poll until the process is recognized
before returning, the way `TestIsBloudDevProcess` does, or retry the identity
read briefly inside `takeoverPreviousDevLoop`. The first keeps the production
function's fail-safe single read; the second is defensible too because the real
caller reads a pid file written by a long-running loop.

**Effort.** Small. **Impact.** Removes the last recurring CLI flake.

### F7. Generated-doc staleness red on main (deterministic, process)

**Symptom.** `Error: README.md dependency graph is stale` and
`Error: README.md is out of date with the catalog`. Also `dependency_graph`
Go unit tests fail in the same job.

**Occurrences.** 9 fast-tier jobs, 2026-09-29 through 2026-10-02, including
on `main`.

**Mechanism.** `depgraph --check` and `catalogdoc --check` run in the fast tier
and fail when `apps/*/metadata.yaml` changed without regenerating the committed
artifacts. That is the gate working as designed. The `main` red is a side
effect of the safety net running *after* CI: a merge that forgot to regenerate
goes red immediately, and `generated-docs.yml` then commits the refresh a
minute later. The next push to `main` is green again.

**Fix options.** Either accept the transient red as intended, or make the
refresh part of the merge (a required check that regenerates and blocks), or
widen the merge-to-main refresh trigger. The cheap fix is the local habit plus
documenting `./bloud depgraph --write && ./bloud catalogdoc --write` in the
contributing guide. The durable fix is to stop accepting a red default branch
at all and let the generator own the files.

**Effort.** Small (habit) to medium (process). **Impact.** A recurring slice of
fast-tier red is this category, and it is fully preventable, so the perceived
reliability improves without touching any test.

### F8. Release and generated-docs workflow bugs (fixed)

Two workflow bugs, now fixed, are worth recording because they account for 9
red `main` runs, 2026-09-25 through 2026-10-01:

- `release.yml` uploaded the rolling asset with `gh release upload latest
  ... --clobber` and got
  `HTTP 422: Cannot upload assets to an immutable release`. Fixed by
  `9d86058`.
- `dependency-graph.yml` ran `git commit -m --no-verify "message"`, so git read
  `--no-verify` as the message and the real message as a pathspec:
  `error: pathspec '...' did not match any file(s) known to git`. Fixed by
  `ea342f7` before the job folded into `generated-docs.yml`.

**Lesson.** A green workflow can be reverted by a later, unrelated edit. The
release job is the only place that pushes to a published release; it deserves a
smoke run in the validation path.

### F9. Complexity and lint gates are the largest source of fast-tier red

**Symptom.** `max-len`, `cyclop`, `funlen`, and `errcheck` violations, plus the
generated-doc checks.

**Occurrences.** Of 56 fast-tier failures since 2026-09-01, roughly 26 are lint
or formatting gates, 9 are generated-doc staleness, and about 13 are the Go
flakes in F4 to F6.

**Mechanism.** These are deterministic. `.golangci.yml` deliberately sits at
the current complexity ceiling ("nothing fails today, but any new code more
complex than the worst function that exists right now errors"). Branches that
lower the ceiling (`chore/frontend-lint-gates`, `chore-lint-gates-repay`) turn
that into a wave of expected red while the code is repaid.

**Fix.** Treat this as a separate, visible workstream rather than flakiness.
The repayment ledger already exists. What matters for perceived reliability is
that the failure text names the gate and the fix command, which it does.

### F10. Startup readiness timeouts (historical, mostly resolved)

**Symptom.** `Timed out waiting for jellyfin to reach "running" after
600000ms`, `Error: no container with name or ID "apps-jellyfin" found`, and
`timed out after 6m0s waiting for prowlarr to reach running`.

**Occurrences.** Concentrated in the 2026-09-01 to 2026-09-05 native-backend
migration window (Jellyfin LDAP login, 35 jobs; install-streaming, 18 jobs) and
in isolated later runs (`TestMediaStackWiring`, 2026-09-29 and 2026-09-30).

**Mechanism.** A cold runner pulls the full Jellyfin image and boots the system
stack while tests wait on a 10-minute budget. On a slow or contended runner the
budget expires. The team has already hardened the worst offender: the
install-streaming leg was pulled from the matrix with a comment that it is "too
sensitive to CI timing", and its behavior is covered by the lifecycle run.

**Fix.** Keep the tightened scope. For the remaining startup waits, prefer
polling an explicit readiness endpoint over a fixed timeout where one exists,
and record the phase the app was in when the budget expired (the API already
returns `last_error`).

## Log signal (DevEx)

The same runs that are flaky are also hard to read, and the two problems share a
cause: raw subprocess output is streamed to the console whether or not it is
signal. Measured on two real jobs:

- A fast-tier job is 953 lines, of which about 200 are runner boilerplate and
  the rest is every `go test` package line, both generator tests printing
  `Error: ...` on their deliberate failure paths, an interactive backend prompt
  from the CLI tests, and two vite builds. A passing run therefore contains text
  that reads as failure, and the one line that says which check failed scrolls
  out of reach.
- An E2E app job is 762 to 1,086 lines. Every matrix leg prints the entire
  frontend build (about 120 lines of emitted chunks), and the install waits
  print a `curl` error per retry (59 lines in one Jellyfin job). The failing
  check is a single Playwright line in the middle.

**Landed.**

- `./bloud validate` now captures each command's combined output, prints one
  line per check plus a pass/fail summary, dumps only the failing commands'
  output, and mirrors everything to `.bloud/logs/validate-<tier>.log`. `--verbose`
  / `-v` (or `BLOUD_VALIDATE_VERBOSE=1`) streams the raw output as before. The
  fast tier's console went from ~900 interleaved lines to about 20 plus the
  failure dumps.
- The E2E harness runs the frontend and host-agent builds through a quiet
  wrapper (`localRunQuiet`), so a success prints `✓ frontend build (2.8s)`, and
  the captured build log accompanies a failure. The install and health retry
  loops no longer print a `curl` error per attempt.

**Still noisy, and why it is acceptable.** The failure-only debug step in the
E2E workflows dumps the journal, container logs, `podman ps`, and `ss` (roughly
150 lines). That output is the evidence a failure needs, and it exists only
when a test failed, so it stays. The image-pull and Actions-cache progress
lines come from the runner and the cache actions, not from this repository.

## Related: the e2e matrix was sized from the latest push only

The reusable `e2e-affected.yml` passed `github.event.before`, the previous tip
of the branch, so `./bloud e2e affected --since $BEFORE` saw only the commits in
the newest push. A pull request that changed `apps/<name>/**` in one commit and
only documentation in the next ran no e2e leg for that app at all: a green
check on a matrix that never ran. `./bloud e2e affected --base <ref>` now
diffs from the merge base with the base branch (every commit the PR
introduces), and the workflow uses `--base origin/main` on a pull request
branch while keeping `--since $BEFORE` on the default branch, where the pushed
range is the right unit. `TestMergeBaseWithCoversEveryCommitInThePR` pins it.

## Prioritized effort

| Order | Item | Type | Effort | Why now |
|---|---|---|---|---|
| P0 | F2 integration LDAP-outpost readiness barrier | Flake | Small | Largest current `main` red; unblocks F3 |
| P0 | F1 forward-auth gate rung in a fresh context | Flake | Small | Most frequent current E2E flake |
| P1 | F4 self-heal status poll | Flake | Small | Recurring fast-tier race |
| P1 | F6 takeover identity poll | Flake | Small | Recurring CLI race |
| P1 | F5 restartable and appclient observe windows | Flake | Small | Removes the remaining Go flakes |
| P2 | F7 generated-doc staleness process | Deterministic | Small to medium | Fully preventable, recurred throughout the window |
| P2 | F8 release-path smoke run | Process | Small | Guards a fix already reverted once |
| P3 | F9 lint-gate repayment | Deterministic | Ongoing | Tracked in the debt ledger |
| P3 | F10 startup budget hardening | Flake | Medium | Largely resolved; keep the scope tight |

## Guardrails to keep it fixed

1. **A flake budget in CI.** Record each failed-then-passed job automatically.
   Today the only signal is a human noticing a rerun, and there were only 23
   reruns in 1,990 runs, so the sample is tiny.
2. **One rerun for infrastructure failures, none for assertions.** A single
   automatic retry on timeout/connection-class jobs would separate load flakes
   from real failures without hiding assertion bugs, at the cost of some wall
   time. The Playwright config currently sets `retries: 0` on purpose; if a
   retry is added, report it, so it cannot quietly become the norm.
3. **A readiness helper per external dependency.** LDAP, the IdP, and each
   app's own health endpoint should each have one wait that every test calls.
   Readiness belongs in one place, not re-derived per test with a different
   budget.
4. **Sized budgets, not generous ones.** Every fixed timeout should say what
   it is waiting for and why the number is what it is. Where the wait is on a
   remote system, poll; where it is on a local phase transition, poll with a
   deadline.
5. **Keep flaky tests named.** A test that fails intermittently should be
   fixed or made deterministic, not skipped silently. The repository already
   avoids blanket retries and skips, which is the right default to preserve.

## Reproducing this analysis

The raw data was pulled with the GitHub CLI:

```bash
# every workflow run, including the attempt number
gh api --paginate 'repos/d-buckner/bloud/actions/runs?per_page=100' \
  --jq '.workflow_runs[] | {id, path, conclusion, created_at, run_attempt, head_branch}'

# failed jobs for a run, and their step conclusions
gh api "repos/d-buckner/bloud/actions/runs/<id>/jobs?per_page=100"

# the full log of one job, which is where the assertion text lives
gh api "repos/d-buckner/bloud/actions/jobs/<job-id>/logs"
```

A same-workflow pass and fail on one commit is the cheapest flake signal, but
it is rare here. The reliable approach is to group failures by assertion text
and look for the same text across unrelated branches and unmodified code paths.
