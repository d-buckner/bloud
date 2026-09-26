# Refactor Plan: Make the Readiness Wait Budget Real

Target: tech-debt ledger item 6 (P1), the `appclient` readiness/wait
contract. Driver: long-term maintainability plus product correctness. Status:
**implemented** on branch `refactor/readiness-wait-budget`.

## Problem Statement

`appclient` lets an app declare how long it is willing to wait for the app it
manages to come up. That declaration did nothing.

`Call.Timeout(d)` stored `timeoutOverride` and nothing in the package ever
read it. Two occurrences in the file: the field declaration and the write.
Meanwhile `WaitPolicy` carried a doc comment saying it was "the default for
calls marked with `Ready()`", and `Ready()` never applied it. `Wait()` asked
`effectivePolicy()`, which returned the client's `c.retry`, which for every
app that did not pass an explicit `WithRetry` was `DefaultRetry`: five
attempts, thirty seconds.

So the three long first-boot waits in the catalog were fiction:

- `apps/immich/api.go` declared `Timeout(5 * time.Minute)` on the server ping
- `apps/affine/api.go` declared `Timeout(5 * time.Minute)` and `Timeout(3 * time.Minute)`
- `apps/hermes/api.go` declared `Timeout(5 * time.Minute)` twice

Each one actually got thirty seconds and five tries.

The consequence is not a slow boot. It is a permanently dead app. The chain,
each link verified in code:

1. Immich's first boot runs database migrations before its HTTP listener answers.
2. The wait gives up at thirty seconds: `wait ... did not become ready after 5 attempts`.
3. `PostStart` returns that error (`apps/immich/configurator.go:126`).
4. The orchestrator sets the node to `StatusError` and the app row to `"error"`.
5. `collectWorkForLevel` skips it forever: *"ERROR is terminal: never retry
   without an explicit status reset."*

The five-minute budget written to prevent exactly this did nothing to prevent it.

There was a second, larger ceiling stacked on top that made the declared number
doubly unreachable: `DefaultPostStartBudget` was 150 seconds, so the
framework cancelled every `PostStart` before a five-minute wait could expire
even if the wait had been wired correctly. Two budgets, both smaller than what
the app asked for, neither of them honest.

No test covered any of it. `pkg/appclient` had twenty tests and not one of
them asserted that a declared timeout did anything, or that a `Ready()` wait
received the long policy its documentation promised. That is why this survived.

## Solution

Split the one confused number into the two numbers that were always there, wire
both, and make the relationship between them a checked invariant rather than a
hope.

**`Timeout(d)` means per-request, and is now true.** The deadline is derived
per attempt in `requestContext`, from the caller's context as parent. The
client-level `http.Client.Timeout` was removed so a call can extend past the
client default as well as shorten it; previously a `Timeout(30s)` on a client
defaulting to 15s would have been silently capped at 15s.

**`Within(d)` is the total wait budget.** It sets the policy `Deadline`.
`Timeout(10s) + Within(5m)` reads as "each probe may take ten seconds, the
whole wait may take five minutes", which is what the apps meant. It is
order-independent with `Ready()` and composes with `WithRetry`.

**`Ready()` now actually defaults to `WaitPolicy`**, as its own documentation
always claimed. An explicit `WithRetry` before or after still wins. This is
the fix for every wait that never declared a policy at all and was silently
running on the short default.

**A per-request timeout is transient, never terminal.** This is the subtle
part. If the derived request context were consulted for terminality, a single
slow probe would return `context.DeadlineExceeded` and `Wait` would treat it
as the end of the wait. `attemptOnce` therefore checks the *caller's*
context for terminality and treats the request's own deadline expiry as an
ordinary transient failure. One slow poll cannot kill a wait that still has
budget.

**The ceiling is single-sourced.** `appclient.MaxWaitBudget` is the longest
wait an app may declare, and the orchestrator's `DefaultPostStartBudget` is
defined as that same constant (raised from 150s to 10 minutes). An app's
declared wait can no longer be shorter than the framework's patience and
longer than its patience at the same time.

**The harness holds the line.** `apps/configtest` gains a rule that AST-walks
the app tree and asserts every declared `Within()` is at or under
`MaxWaitBudget`. It fails on a budget it cannot evaluate rather than skipping
it, so the rule cannot be dodged by writing the duration in a shape the
checker does not understand. Verified to fail: setting Immich's wait to 15
minutes produces the error, and reverting it clears it.

## Commits

1. Add `MaxWaitBudget` to `pkg/appclient/retry.go` as the single source for
   the longest honourable readiness wait, documented with why it exists.
   → verify: `cd services/host-agent && go build ./pkg/appclient/...`

2. Make `Timeout(d)` a real per-request deadline: add `requestContext` to
   `attempt.go`, derive the per-attempt deadline from it in `attemptOnce`
   and `attemptStream`, and check the caller's context (not the derived one)
   for terminality. Remove the client-level `http.Client.Timeout` from
   `client.go` so the per-request value is authoritative in both directions.
   → verify: `cd services/host-agent && go test ./pkg/appclient/...`

3. Add `Within(d)` to `call.go` for the total wait budget, with the
   `budgetErr` field that records a budget above `MaxWaitBudget`, and surface
   it at the top of `Wait` so an unreachable wait fails loudly.
   → verify: `cd services/host-agent && go test ./pkg/appclient/...`

4. Make `Ready()` default the call's policy to `WaitPolicy` when no explicit
   policy is set, honouring the contract its doc comment already stated.
   → verify: `cd services/host-agent && go test ./pkg/appclient/...`

5. Add the wait-budget test file: per-request deadline fires and cancels
   server-side, `Timeout` extends past the client default, `Ready` defaults
   to `WaitPolicy`, `WithRetry` still wins, `Within` sets the deadline,
   over-budget fails at `Wait`, a per-request timeout does not end a wait,
   a wait polls past five attempts, and a cancelled caller is still terminal.
   → verify: `cd services/host-agent && go test ./pkg/appclient/... -v`

6. Raise the orchestrator's `DefaultPostStartBudget` to `appclient.MaxWaitBudget`
   and document the coupling.
   → verify: `cd services/host-agent && go test ./internal/engine/orchestrator/...`

7. Convert the five long first-boot waits from `Timeout` to `Within` in
   immich, affine, and hermes. Home Assistant's `Timeout(10s)` stays as
   `Timeout`: it is on a one-shot `Do()`, where per-request is the correct
   meaning, and it now actually applies.
   → verify: `cd apps && go test ./...`

8. Add the harness rule to `apps/configtest`: walk the app tree, evaluate
   every `Within()` budget, fail above the ceiling, fail on unevaluable.
   → verify: `cd apps && go test ./configtest/... -run WaitBudget -v`

9. Update the ledger: item 6 closed with the failure chain and the fix, and
   repayment-plan section 3 marked done.
   → verify: `npm run check:docs-links && npm run lint:prose`

10. Raise the startup convergence gate so it cannot be tripped by a single
    node. Raising `DefaultPostStartBudget` to 10 minutes made the hardcoded
    10-minute startup gate in `waitForSystemConvergence` smaller than one
    node's own budget: a node that spent its full budget would trip the
    startup timeout and `os.Exit(1)` the whole control plane, which is a
    worse blast radius than the 150 seconds it replaced. The gate is now
    `appclient.MaxWaitBudget + 5 * time.Minute`, derived from the same
    constant so the two cannot drift, with a test that fails if the gate is
    ever set at or below the per-node budget.
    → verify: `cd services/host-agent && go test ./cmd/host-agent/... -run StartupGate`

11. Run the fast tier and the race detector over the touched packages.
    → verify: `./bloud validate --tier fast` and
    `cd services/host-agent && go test -race ./pkg/appclient/... ./internal/engine/orchestrator/...`

## Decision Document

- `Timeout` and `Within` are separate methods with separate meanings, not one
  overloaded method. The alternative considered was letting `Timeout` mean
  the wait budget when followed by `Wait()`; rejected because a method whose
  meaning depends on which terminal verb closes the chain is a trap for the
  next reader, and the chain is multi-line so the terminal verb is not even
  visible at the call.
- The per-request deadline is applied through a derived context, not through
  `http.Client.Timeout`. This is what lets a call extend past the client
  default instead of being capped by it, and it is what makes the value real
  for every attempt rather than for the client's lifetime.
- Terminality belongs to the caller's context. A request-level deadline
  expiry is classified as transient so it participates in the wait's normal
  retry budget. This was a deliberate choice against the more obvious
  implementation, which would have made a slow probe fatal to the wait.
- `Ready()` sets the default policy rather than `Wait()` choosing one at
  terminal time, so the policy is visible on the call builder and an
  explicit `WithRetry` in either order overrides it.
- `MaxWaitBudget` lives in `pkg/appclient`, not in `pkg/configurator`,
  because `pkg/configurator` already imports `pkg/appclient`; putting it the
  other way round would be an import cycle. The orchestrator's
  `DefaultPostStartBudget` is defined as `appclient.MaxWaitBudget`, so the
  two ceilings cannot drift.
- The startup convergence gate is derived from the same constant
  (`MaxWaitBudget + 5m`) rather than staying an independent literal. Raising
  the per-node budget without touching the gate would have made the gate the
  smallest number in the relationship, so one slow or hung node could trip
  it and take the control plane down. Three timeouts that describe one
  quantity now come from one place: the app's `Within`, the node's
  `PostStartBudget`, and the startup gate.
- An over-budget `Within()` fails at `Wait` time rather than being clamped.
  Clamping reproduces the original sin: a declared number that is not what
  actually happens.
- The harness rule reads source with `go/ast` rather than requiring each app
  to re-declare its budget in a manifest. A second declaration site would be
  a second thing that can drift from the first.
- The AST evaluator fails loudly on a duration shape it cannot evaluate.
  Skipping an unevaluable budget would make the check quietly incomplete,
  which is how the original gap survived a suite of twenty tests.

## Testing Decisions

A good test here asserts what a caller observes, not how the deadline is
implemented. The tests never inspect `timeoutOverride` or `budgetErr`
directly except where the assertion is about the built policy itself.

The per-request deadline test is behavioural in the strongest available sense:
the test server watches its own `r.Context().Done()` and reports whether the
client hung up before the handler finished. That distinguishes "the client
really cancelled" from "the test merely saw an error", which a status-code
assertion cannot do.

The regression that matters most is `TestWait_PerRequestTimeoutIsTransientNotTerminal`:
a first probe that blows its per-request deadline, followed by a good one,
must converge rather than abort. This is the case a naive implementation of
"wire the timeout" gets wrong, and it is the case that would have turned a
fixed bug into a worse one.

`TestWait_PollsPastFiveAttempts` pins the specific number that was the bug:
a `Ready()` wait must exceed five attempts, because five was `DefaultRetry`'s
cap and reaching it was what killed cold boots.

The harness rule is a static check with a runtime guarantee: it was verified
to fail by setting a budget above the ceiling and watching it fail, then
reverted. A guard that has never been seen to fail is a rumour.

Prior art in this repo, same shape:

- `apps/configtest/configtest.go` (from #121): the conformance harness that
  turned configurator doc comments into assertions. This adds one more rule
  to the same table.
- `apps/vaultwarden/configurator_test.go`: loads its own `metadata.yaml` and
  asserts the code matches it, the cross-file consistency pattern.
- `npm run check:image-pins`: a cross-file rule promoted to a gate, including
  the "fail on an exception that matches nothing" discipline that the
  unevaluable-budget rule copies.
- `services/host-agent/internal/wire/completeness_test.go`: pinning that a
  wiring is complete rather than assumed.

## Out of Scope

- The other open P1 items: container drift never repaired while the process
  is alive (item 8), `Ensure` force-removing before pull with no rollback
  (item 9), and the health surface's blindness to the Podman socket
  (item 10). Each is its own change; item 8 and 9 in particular touch the
  reconcile loop and want live-Podman verification.
- The P2 inventory: built-in primary host persistence, system-app hiding by
  category, the nil-wired sharing and system modules, `ClearAppDataIntent`,
  the `DeriveSecret` fallback, hand-assembled Traefik YAML, `PlanInstall`
  blockers, `primaryContainerNode` ordering, the icon path traversal,
  network and orphan sweeping, and the `GetCatalog` cached-struct race.
- Changing any app's actual boot behaviour. No app is configured
  differently; only the honesty of how long Bloud is willing to wait changes.
- Home Assistant's stale-trust probe semantics, which came out of the #121
  refactor unchanged and stay unchanged here. Its `Timeout(10s)` now applies
  per request, which is what it always claimed to do.
- Per-app configurable wait budgets from `metadata.yaml`. `MaxWaitBudget` is
  a global ceiling; making it per-app is a larger contract change with no
  current demand.
- Frontend, CLI, and packaging.

## Further Notes

The instructive part is why twenty tests missed this. Every test in
`pkg/appclient` tested the machinery that was wired. Nobody tested the
modifier that was not. A method that accepts a value, stores it, and returns
the receiver for chaining looks correct in a code review and passes every
existing test, because passing a test requires the value to be read and it
was read by nothing.

That is the same failure shape as the C3 regression from the #121 work: the
ledger recorded that seven no-op `Remove` methods were deleted, and seven
came back within days in newly written apps, because the interface made the
wrong thing look like the expected shape. The answer both times was the same:
do not rely on the next author noticing, make a test notice.

One thing this refactor does not fix, worth stating plainly: a wait that
exhausts its budget still lands the app in `ERROR`, and `ERROR` is still
terminal by design. This change makes the budget honest so the terminal state
is reached only when the app genuinely failed to come up, rather than when it
came up slowly. Whether `ERROR` should be retryable with backoff is a
different and larger question about the reconciler, and it is not answered here.
