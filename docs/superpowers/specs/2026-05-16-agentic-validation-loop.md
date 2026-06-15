# Spec: Agentic Local Validation Loop

**Date:** 2026-05-16
**Status:** Proposed

## Problem

Bloud has useful test coverage, but the local validation loop is fragmented. Fast unit checks pass, while the higher-confidence VM and end-to-end paths are split across root npm scripts, Playwright packages, Proxmox CLI commands, stale docs, and missing lifecycle glue.

The result is that an agent cannot reliably answer the most important engineering question after a change:

> What is the smallest trustworthy validation path for this diff, and what should run next if confidence is still low?

Current observed issues:

- Root scripts expose `test:integration`, but `integration/package.json` points at missing `../scripts/run-integration-tests`.
- `smoke/` and `integration/` are not root npm workspaces, despite being core validation surfaces.
- Docs describe `./bloud test` as implemented, but the CLI does not currently expose a `test` command.
- `./bloud smoke`, `./bloud push`, `./bloud rebuild`, and Proxmox snapshots are the real high-value VM loop, but they are not unified behind a validation contract.
- `npm run build` can fail for local toolchain reasons unrelated to source correctness, which makes build validation brittle.
- Nix checks require a Nix-capable environment, which may not exist in the local shell.
- Slow/destructive Playwright flows exist but are skipped by default without a first-class command tier that opts into them.

## Goal

Create a single, agent-friendly local validation system that:

1. Selects the right checks from the changed files.
2. Runs fast deterministic checks first.
3. Escalates to VM, smoke, clean-state, or full ISO validation only when warranted.
4. Produces a machine-readable validation ledger after every run.
5. Makes app validation uniform so new apps can be tested through the same contract.

The primary user-facing command should be:

```bash
./bloud validate [--tier fast|changed|vm|clean|full] [--app <name>] [--json] [--explain] [--dry-run]
```

Default tier is `changed` when no `--tier` is specified. This answers the most common agentic question: "what should I validate for this diff?"

## Non-Goals

- Replacing Go, Vitest, Playwright, Nix, or Proxmox tooling.
- Making every test parallel immediately.
- Requiring Nix on non-Nix local hosts.
- Running destructive install/uninstall flows in the default fast loop.
- Solving CI release validation completely; this spec focuses on local agentic engineering.

## Exit Code Contract

```
0: All selected checks passed.
1: One or more checks failed.
2: Infrastructure unavailable (no VM, no nix, no network) and the tier requires it.
3: Invalid arguments, manifest error, or internal validation system failure.
```

Exit code 2 means the tier *could not produce a trustworthy result*. Agents should treat exit 2 as "escalation blocked" rather than "passed" or "failed."

## Command Flags

- `--tier <level>`: Select validation tier. Default: `changed`.
- `--app <name>`: Restrict validation to a single app.
- `--json`: Machine-readable output only (no streaming, no prompts).
- `--explain`: Print why each command was selected (which changed files triggered it). Can combine with execution.
- `--dry-run`: Show the execution plan without running anything. Useful for agents to preview before committing to a long run.
- `--since <ref>`: Override diff base (default: uncommitted changes + staged).
- `--no-vm`: Skip any commands that require a running VM, even if the tier would normally include them.
- `--include-destructive`: Opt into uninstall/clean-state flows that are skipped by default.

## Command Contract

### `./bloud validate --tier fast`

Runs checks that should be cheap and deterministic on the local machine:

```bash
go test ./...                         # services/host-agent
go test ./...                         # apps
go test ./...                         # cli
go test ./...                         # services/installer
go test -race ./internal/orchestrator/...  # services/host-agent
npm run test --workspace=@bloud/host-agent-web
npm run check --workspace=@bloud/host-agent-web
npm run check --workspace=@bloud/installer-web
```

Expected runtime target: under 30 seconds on a warm machine.

### `./bloud validate --tier changed`

Infers affected validation from the diff. It should:

- Map files under `services/host-agent/**` to the host-agent Go module and web workspace as appropriate.
- Map files under `apps/<name>/**` to app Go tests, app metadata/module checks, and `apps/<name>/test.ts` if present.
- Map files under `nixos/**`, `flake.nix`, or app `module.nix` to Nix evaluation and VM rebuild validation.
- Map files under `services/host-agent/web/src/service-worker/**` to Vitest service-worker tests and embedded-app Playwright tests.
- Map files under `smoke/**` or `integration/**` to Playwright list/config validation and the relevant suite.

**Inference fallback rule:** If a changed file maps to multiple risk areas, or maps to no known validation path, the system must escalate to the next tier rather than silently skip validation. A changed file with no inferred checks is a signal that inference coverage is incomplete — this should be reported in the ledger and trigger `nextRecommendedTier`.

Expected runtime target: under 2 minutes unless Nix or VM escalation is required.

### `./bloud validate --tier vm`

Runs against an already-running Proxmox dev VM where possible.

Behavior:

- For host-agent Go changes, run `./bloud push`, wait for health, then run targeted smoke/app tests.
- For NixOS or app module changes, run `./bloud rebuild`, wait for health, then run targeted smoke/app tests.
- For frontend-only changes, run the relevant Playwright smoke projects against the running VM.
- If `--app <name>` is present, run only that app's install/embed/auth contract.

Expected runtime target: 1-5 minutes depending on whether push or rebuild is required.

### `./bloud validate --tier clean`

Restores a known clean Proxmox snapshot before validation.

Behavior:

1. Restore snapshot, default `base-installed`.
2. Start VM and wait for health checks.
3. Apply the minimal change path: `push` for Go-only changes, `rebuild` for Nix/module changes.
4. Install and validate selected app or app set.
5. Leave VM running for inspection unless `--destroy` is added later.

Expected runtime target: 5-10 minutes.

### `./bloud validate --tier full`

Runs the highest-confidence local validation:

1. Fast tier.
2. Nix-capable checks where available.
3. ISO build/deploy path.
4. First-run setup.
5. Full smoke suite.
6. Selected integration suite.

This tier may require Proxmox and a builder host.

## App Validation Contract

Every app should expose the same validation shape.

Required for each app:

- `apps/<name>/metadata.yaml`
- `apps/<name>/module.nix`
- `apps/<name>/integration.md` or `INTEGRATION.md`

Required when app-specific runtime behavior exists:

- `apps/<name>/configurator.go`
- `apps/<name>/configurator_test.go`
- `apps/<name>/test.ts`

Standard app checks:

1. Catalog loads app metadata.
2. Dependency plan is valid.
3. Nix module evaluates.
4. Install request reaches `running`.
5. `/embed/<name>/` responds.
6. Iframe loads without critical CSS/JS failures.
7. Auth strategy behaves as declared:
   - `native-oidc`: app redirects or signs in through its native OIDC flow.
   - `forward-auth`: unauthenticated access redirects top-level window through Authentik.
   - `ldap`: app login succeeds or reaches expected LDAP-backed auth state.
   - none: app loads without auth redirects.
8. Uninstall is validated only in clean or full tier unless explicitly requested.

### App Validation Levels

Not all apps reach full validation maturity immediately. Each app declares its current level:

| Level | Checks covered | When to use |
|-------|---------------|-------------|
| `metadata-only` | Steps 1-3 | App has module.nix and metadata but no runtime tests yet |
| `embed` | Steps 1-6 | App installs and embeds but auth isn't validated |
| `full` | Steps 1-8 | App has complete auth and lifecycle validation |

Declare in `validation.yaml` or default to `full` if the app has a `test.ts`. Apps at lower levels should have a tracking issue for reaching `full`.

## Validation Manifest

Add a repository-owned manifest that gives agents a stable contract:

```yaml
# validation.yaml
tiers:
  fast:
    commands:
      - id: go-host-agent
        cwd: services/host-agent
        run: go test ./...
      - id: go-host-agent-race
        cwd: services/host-agent
        run: go test -race ./internal/orchestrator/...
      - id: go-apps
        cwd: apps
        run: go test ./...
      - id: go-cli
        cwd: cli
        run: go test ./...
      - id: go-installer
        cwd: services/installer
        run: go test ./...
      - id: web-unit
        cwd: .
        run: npm run test --workspace=@bloud/host-agent-web
      - id: web-check-host-agent
        cwd: .
        run: npm run check --workspace=@bloud/host-agent-web
      - id: web-check-installer
        cwd: .
        run: npm run check --workspace=@bloud/installer-web

apps:
  miniflux:
    auth: native-oidc
    validation-level: full
    files:
      - apps/miniflux/**
    tests:
      unit: []
      playwright:
        - apps/miniflux/test.ts

  qbittorrent:
    auth: forward-auth
    validation-level: embed
    files:
      - apps/qbittorrent/**
    tests:
      playwright:
        - apps/qbittorrent/test.ts
```

A minimal `validation.yaml` is introduced in Phase 2 alongside the command implementation. The manifest starts incomplete — apps without entries fall back to convention-based discovery (`apps/<name>/test.ts`, `apps/<name>/configurator_test.go`). The manifest grows as behavior stabilizes.

## Validation Ledger

Every run should write a ledger:

```text
.bloud/validation/latest.json
.bloud/validation/<timestamp>.json
```

Required fields:

```json
{
  "startedAt": "2026-05-16T00:00:00Z",
  "finishedAt": "2026-05-16T00:01:12Z",
  "tier": "changed",
  "exitCode": 0,
  "apps": ["immich"],
  "changedFiles": ["apps/immich/module.nix"],
  "riskAreas": ["nix-module", "app-install", "embed"],
  "confidence": "medium",
  "confidenceReason": "nix-module risk area requires VM validation but --no-vm was not set and no VM is available",
  "commands": [
    {
      "id": "go-apps",
      "cwd": "apps",
      "command": "go test ./...",
      "status": "passed",
      "durationMs": 4045,
      "exitCode": 0
    }
  ],
  "skipped": [
    {
      "id": "nix-flake-check",
      "reason": "nix executable not available"
    }
  ],
  "unmappedFiles": [],
  "artifacts": [
    "smoke/playwright-report/index.html"
  ],
  "nextRecommendedTier": "vm"
}
```

**Confidence levels:**

- `high`: All inferred risk areas are covered by passing checks. No escalation needed.
- `medium`: Some risk areas could not be fully validated (e.g., Nix changes without a VM). Results are partially trustworthy.
- `low`: Critical risk areas were skipped or inference could not map changed files. Escalation is strongly recommended.

**Ledger retention:** Keep the 20 most recent ledger files. Prune on every write. No configuration needed.

The ledger is the handoff between automated agents, humans, and CI. If a run fails, the next agent should not need to rediscover what already happened.

## Resolved Decisions

These were open questions that are now resolved to unblock implementation:

1. **`smoke/` vs `integration/`**: `smoke/` remains a standalone Playwright package targeting first-run and running-system validation. `integration/` is deleted — its useful tests move into `smoke/tests/` under appropriate Playwright projects. There is no meaningful architectural distinction for a single-host appliance.

2. **`./bloud test`**: Not implemented. `./bloud validate` replaces it. Documentation claiming `./bloud test` exists is removed.

3. **`validation.yaml` timing**: A minimal manifest is introduced in Phase 2. It starts incomplete and grows. Convention-based fallbacks handle apps not yet in the manifest.

4. **Clean tier snapshots**: Always restore `base-installed`. App-specific snapshots are a future optimization, not needed for v1.

5. **Nix check delegation**: Run locally when `nix` is available. When unavailable, report as skipped with confidence degradation. Builder-host delegation is a future enhancement.

## Implementation Plan

### Phase 1: Clean Up Stale Entry Points

Concrete mechanical fixes (no design decisions remain):

- Remove `integration/` directory. Move any valuable tests to `smoke/tests/`.
- Remove root `test:integration` script from `package.json`.
- Remove any docs claiming `./bloud test` exists.
- Remove `scripts/run-integration-tests` reference.
- Fix Go debug symbol build issue on macOS (strip `-s -w` ldflags for local builds).

### Phase 2: Implement `./bloud validate`

- Add `validate` command dispatch in `cli/main.go`.
- Implement tiers in a new CLI module, for example `cli/validate.go`.
- Introduce minimal `validation.yaml` with fast-tier commands and known apps.
- Stream command output live (suppress with `--json`).
- Record structured results to `.bloud/validation/` with retention pruning.
- Implement `--json`, `--explain`, and `--dry-run` flags.
- Implement exit code contract (0/1/2/3).

### Phase 3: Add Changed-File Inference

- Use git status/diff to infer touched paths.
- Map paths to modules, workspaces, apps, and VM risk areas.
- Implement inference fallback: unmapped files trigger escalation recommendation, never silent skips.
- Compute confidence level from coverage of inferred risk areas.
- Print the inferred plan before running unless `--json` is used.
- `--dry-run` prints the plan and exits with code 0.

### Phase 4: Normalize App Tests

- Make `apps/<name>/test.ts` the canonical app E2E contract.
- Ensure smoke app projects and integration app tests share helper logic where possible.
- Convert skipped destructive tests into explicitly tagged tests.
- Add app contract checks for any app with `configurator.go` but no unit tests.

### Phase 5: Improve Reports And Parallelism

- Use Playwright blob reports for shardable suites and merge them into one HTML report.
- Add concise timing summaries for every tier.
- Persist slowest commands and slowest tests in the ledger.
- Add optional parallel execution only where state isolation is guaranteed.

## Acceptance Criteria

- `./bloud validate` (no flags) runs the `changed` tier against the current diff.
- `./bloud validate --tier fast` succeeds on a correctly configured local checkout without Proxmox.
- `./bloud validate --tier changed` explains why each selected command was chosen (with `--explain`).
- `./bloud validate --dry-run` prints the execution plan without running anything.
- `./bloud validate --tier vm --app miniflux` validates a single app against a running Proxmox VM.
- Exit codes follow the contract: 0 = passed, 1 = failed, 2 = infra unavailable, 3 = usage error.
- Missing external tools such as `nix` are reported as skipped with confidence degradation, not silent failures.
- Changed files that map to no known validation path are reported in `unmappedFiles` and degrade confidence.
- `integration/` directory no longer exists; its tests live in `smoke/tests/`.
- Documentation no longer claims `./bloud test` exists.
- Every validation run writes `.bloud/validation/latest.json` and prunes to 20 retained ledgers.
- The ledger includes `confidence` and `confidenceReason` fields.
- A failed Playwright run links to the exact report or artifact path.
- Adding a new app has a documented validation checklist and a consistent command.

## Open Questions

- Should parallel execution in Phase 5 use goroutines within the CLI or delegate to external parallelism (e.g., GNU parallel, make -j)?
- What is the right granularity for `riskAreas`? Current set: `go-unit`, `web-unit`, `nix-module`, `app-install`, `embed`, `auth`, `service-worker`. Is this stable enough to commit to as a contract?
- Should `./bloud validate` integrate with `bd` (beads) to auto-close validation-related issues when all checks pass?
