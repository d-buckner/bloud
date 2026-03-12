# Design: `./bloud smoke` — Local Change Validation

**Date:** 2026-03-11
**Status:** Approved

## Problem

There is no fast local way to validate that a change works end-to-end before committing. The existing `integration/` test suite is designed around Proxmox VM lifecycle with pre-installed apps and different ports — it does not cover the fresh-install flow (setup wizard → create user → install app → verify embed).

## Goal

A single command — `./bloud smoke` — that builds a fresh ISO, deploys it to the Proxmox test VM, completes the first-run setup, installs Miniflux, and verifies the Bloud shell correctly embeds it via visual regression.

## Command

```
./bloud smoke [--update-snapshots]
```

- **Proxmox mode only** (requires `BLOUD_PVE_HOST`)
- Always implies `--build` — builds a fresh ISO every run, no `--skip-deploy`
- `--update-snapshots` — passes `--update-snapshots` to Playwright to refresh committed baselines
- VM is **left running** after completion for manual inspection

## Directory Structure

```
smoke/
  package.json                          # playwright dependency
  playwright.config.ts                  # baseURL: http://bloud.local, single worker
  lib/
    api.ts                              # slim API client
  tests/
    smoke.spec.ts                       # single end-to-end test
    smoke.spec.ts-snapshots/            # committed baseline PNGs
```

No global setup/teardown — the CLI handles VM lifecycle before Playwright runs.

## Test Flow (`smoke/tests/smoke.spec.ts`)

1. **Wait for setup readiness** — poll `GET /api/setup/status` until `authentikReady: true` (up to 5 minutes, 5s intervals)
2. **Create initial user** — `POST /api/setup/create-user` with fixed test credentials; response sets session cookie
3. **Install Miniflux** — `POST /api/apps/miniflux/install`
4. **Wait for Miniflux** — poll `GET /api/apps/installed` until Miniflux status is `running` (up to 5 minutes, 10s intervals)
5. **Navigate** — browser navigates to `http://bloud.local/apps/miniflux`
6. **Screenshot** — `expect(page).toHaveScreenshot('miniflux.png')` — full page, no masking

## API Client (`smoke/lib/api.ts`)

Thin wrapper around Playwright's `APIRequestContext` (not raw `fetch`) — cookie handling is automatic; the session cookie set by `create-user` is carried into all subsequent requests. Methods:

- `waitForSetupReady(timeout)` — polls `GET /api/setup/status` until `authentikReady: true`
- `createUser(username, password)` — POSTs to `/api/setup/create-user`
- `installApp(name)` — POSTs to install endpoint
- `waitForAppRunning(name, timeout)` — polls `GET /api/apps/installed` until status is `running`

## Visual Regression

- Playwright's built-in `toHaveScreenshot()` with default pixel diff threshold
- Baseline PNGs committed to `smoke/tests/smoke.spec.ts-snapshots/`
- On failure: diff image + actual screenshot saved to `smoke/test-results/`
- To update baselines: `./bloud smoke --update-snapshots`

## CLI Integration (`cli/pve.go`)

New `smoke` case in the Proxmox command dispatcher:

1. Call existing `cmdStartPVE(args []string)` with `["--build", "--install"]` — ISO build + VM deploy + health checks
2. Run `npm ci` in `smoke/` if `node_modules` absent
3. Run `npx playwright test` (with `--update-snapshots` if flag passed)
4. Exit with Playwright's exit code

## What This Validates

- The setup wizard completes successfully on a fresh deploy
- Session cookie auth works after user creation
- Miniflux installs and reaches `running` state
- The Bloud shell at `/apps/miniflux` renders correctly — Miniflux is embedded (not showing a login redirect, not embedding Bloud inside itself, shell chrome present)

## Out of Scope

- Testing other apps (just Miniflux for now)
- Masking dynamic iframe content (accepted flakiness risk, revisit if needed)
- Native NixOS mode (`./bloud smoke` only works with `BLOUD_PVE_HOST` set)
- VM teardown after test
