# Automatic Updates

## Context & Motivation

Bloud currently has no in-place update mechanism. The only way to update a running system is to
re-flash a new ISO — impractical for production deployments. Updates need to be first-class.

There are three distinct scopes of updates:

1. **System updates** — the NixOS configuration, host-agent binary, and frontend
2. **App container image updates** — individual app containers (Jellyfin, Radarr, etc.)
3. **Configuration reconciliation** — re-applying app configurator logic after a system change

All three are handled as a single atomic operation: one update action updates everything together.

---

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Update trigger | Manual (user-initiated) | User controls timing; no surprise restarts |
| Update channels | Single stable channel | Simplest to reason about; no channel management |
| App image updates | Bundled with system update | Coordinated, tested sets of versions; avoids silent breakage |
| Image pinning | Digest-pinned (`image:version@sha256:...`) | `:latest` tags don't trigger pulls; digests enforce pull |
| Update mechanism | `nixos-rebuild switch --flake github:owner/bloud/stable#installed` | Pure Nix; atomic; built-in rollback via generations |
| Pre-built artifacts | `fetchurl` with pinned SHA256s in `host-agent.nix` | CI embeds SHAs; no vendorHash fragility |
| Rollback trigger | Auto on hard failure + manual for soft issues | Auto catches build/health failures; manual covers runtime issues |
| Pre-update checks | Disk space + backup status | Catch the most common failure causes before touching anything |

---

## Flake Attribute for Production Systems

The existing installed system rebuilds using a hostname-keyed `nixosConfigurations` attribute
(e.g. `#bloud`). For remote flake updates the hostname is unknown at flake authoring time.

The flake exposes a fixed `nixosConfigurations.installed` attribute that is the canonical
production system configuration (parallel to `nixosConfigurations.dev-server`). All in-place
update commands target this attribute:

```
nixos-rebuild switch --flake github:owner/bloud/stable#installed --impure
```

The host-agent hardcodes `#installed` as the attribute name for remote update flake refs.
Local dev rebuilds continue using `#dev-server`.

`Rebuilder` currently derives the flake attribute from `r.hostname`
(`fmt.Sprintf("%s#%s", r.flakeURI(), r.hostname)`). The update path requires an override.
`Rebuilder` gets a new optional `nixosAttribute string` field; when set, it is used instead
of the hostname in `Switch()`. The update handler constructs a `Rebuilder` with
`nixosAttribute: "installed"` and `flakePath: "github:owner/bloud/stable"`.

Because `DryRun` delegates to `Switch` internally (`r.dryRun = true` then calls `Switch`),
the `nixosAttribute` override in `Switch` automatically covers the dry-run step — no separate
handling is needed in `DryRun`.

`DryRun` is currently on the concrete `*Rebuilder` struct but absent from `RebuilderInterface`.
It must be added to the interface so the update handler can call it without bypassing the
interface (which would break testability).

---

## Update Flow

```
User triggers update from UI
         │
         ▼
 Pre-flight checks
 ├── sufficient disk space?
 └── recent backup exists?
         │
         ▼
 Write rollback snapshot (apps-rollback-snapshot.json)
 (LoadCurrent() → snapshot before any state changes)
         │
         ▼
 Dry-run against remote flake (detect eval failures before pulling images)
 (Rebuilder{nixosAttribute:"installed", flakePath:"github:..."}.DryRun())
         │
         ▼
 Pull new container images
 (podman pull image@sha256:digest for each pinned app)
         │
         ▼
 nixos-rebuild switch --flake github:owner/bloud/stable#installed --impure
         │
    ┌────┴────┐
  fails     succeeds
    │            │
    ▼            ▼
 Auto-rollback  systemctl --user restart bloud-host-agent.service
 (log reason)  (new binary takes over; client reconnects)
                    │
                    ▼
               systemctl --user restart bloud-apps.target
               (systemd resolves ordering via existing path-unit deps)
                    │
                    ▼
               Integration config refresh
               (re-read metadata.yaml, update integration_config in DB)
                    │
                    ▼
               Post-update reconciliation
               (ReconcileWithResults: PreStart + PostStart for all apps)
                    │
               ┌────┴────┐
             errors    clean
               │            │
               ▼            ▼
          Surface in UI  Mark update complete
          (do NOT auto-rollback — system is up)
```

Hard failures (nixos-rebuild error, critical service health check failure) trigger automatic
rollback. Soft failures (an app's PostStart produced errors, a non-critical integration
misconfigured) are surfaced in the UI for the user to act on manually.

### Host-Agent Self-Restart

After `nixos-rebuild switch` completes, the new host-agent binary is on disk but the running
process is still the old version. The update handler calls
`systemctl restart bloud-host-agent.service` before reconciliation, which causes systemd to
kill the current process and start the new binary.

The SSE stream to the client will disconnect at this point. The client reconnects and polls
`GET /api/system/update/status` to track the remaining steps.

#### Durable Update State

Before triggering the self-restart, the handler writes an update-state record to the database.
The new binary reads this on startup (in a new `ResumeUpdate()` check called from `main.go`
after the orchestrator is initialised) and continues from the next step.

Minimal schema addition to `schema.sql`:

```sql
CREATE TABLE system_update_state (
    id          INTEGER PRIMARY KEY CHECK (id = 1),  -- single row
    status      TEXT NOT NULL,  -- 'in_progress' | 'completed' | 'failed'
    step        TEXT NOT NULL,  -- last completed step: 'rebuild' | 'restart' | 'reconcile'
    version     TEXT NOT NULL,  -- target version being applied
    started_at  TIMESTAMP NOT NULL,
    updated_at  TIMESTAMP NOT NULL
);
```

On startup, `ResumeUpdate()` checks for a row with `status = 'in_progress'` and runs the
remaining steps from after the last completed `step`. If no row exists (or `status` is not
`in_progress`), startup proceeds normally.

---

## App Container Image Pinning

### Current State (Problem)

All container apps use `:latest` image tags:

```nix
image = "jellyfin/jellyfin:latest";       # apps/jellyfin/module.nix
image = "linuxserver/radarr:latest";      # apps/radarr/module.nix
image = "actualbudget/actual-server:latest"; # apps/actual-budget/module.nix
```

Two problems: (1) podman won't pull a new image when the tag is unchanged, so a `nixos-rebuild
switch` with `:latest` is a no-op for container images; (2) `:latest` is unpinned, making
deployments non-reproducible.

### Target State

Each `module.nix` pins both version tag and digest:

```nix
image = "jellyfin/jellyfin:10.9.0@sha256:abc123...";
```

This ensures:
- podman pulls the exact image on every deploy (digest mismatch → pull)
- reproducible across installs and updates
- image update requires an explicit change in module.nix (intentional, not accidental)

### Version Manifest

A `apps/versions.json` file tracks current pinned versions for all apps:

```json
{
  "jellyfin":      { "image": "jellyfin/jellyfin",           "tag": "10.9.0",  "digest": "sha256:abc..." },
  "radarr":        { "image": "linuxserver/radarr",          "tag": "5.16.0",  "digest": "sha256:def..." },
  "sonarr":        { "image": "linuxserver/sonarr",          "tag": "4.0.13",  "digest": "sha256:..." },
  "authentik":     { "image": "ghcr.io/goauthentik/server",  "tag": "2025.10.3", "digest": "sha256:..." }
}
```

A CI script (`scripts/update-app-versions.sh`) checks Docker Hub / GHCR for new tags, updates
the manifest, regenerates the `image = ...` lines in each `module.nix`, and opens a PR. Version
bumps are reviewed before merging — deliberate, not automatic.

### Authentik Exception

Authentik already uses a pinned version tag (`2025.10.3`). It stays pinned and updates are
handled manually since Authentik upgrades often require migration steps.

---

## Pre-Built Artifacts for In-Place Updates

The existing `host-agent.nix` detects pre-built artifacts via `builtins.pathExists "../../build/host-agent"`.
This works for ISO builds (artifacts exist in the source tree) but not for remote flake evaluation
(`github:owner/bloud/stable` has no `build/` directory).

### Updated `host-agent.nix` Logic

```
Is BLOUD_FLAKE_PATH set? (deployed system, running from store)
  Yes → use builtins.storePath to reference already-deployed package (existing logic)

Is build/host-agent present? (local ISO build or CI)
  Yes → package the local artifacts (existing logic)

Neither → fetchurl mode (in-place update from remote flake)
  → download host-agent binary from GitHub release at flake rev
  → download frontend tarball from GitHub release at flake rev
  → verify SHA256 (embedded in host-agent.nix by CI at release time)
```

### CI Release Workflow Sequence

The SHA must be computed from the same build output that is uploaded to the release. The ordering
that avoids the chicken-and-egg problem:

```
1. Build Go binary + frontend (produces artifacts in build/)
2. Compute SHA256 of each artifact (sha256sum)
3. Write fetchurl SHAs + release URLs into nixos/packages/host-agent.nix
   (URLs reference the tag that will be pushed in step 6, e.g.
    https://github.com/owner/bloud/releases/download/2026.03.15/host-agent)
4. Commit: "chore: set release artifacts for 2026.03.15"
5. Build ISO from this commit (uses local build/ artifacts, not fetchurl)
6. Push tag 2026.03.15 — GitHub release is created, artifacts uploaded
   (the URLs embedded in step 3 now resolve correctly)
```

Steps 5 and 6 happen after the SHA commit, so the ISO is built from the commit that contains
correct SHAs. In-place updates evaluate the flake at the tag (step 6), which points to the
same commit — SHAs match the uploaded artifacts.

---

## Service Restart Ordering

After `nixos-rebuild switch`, the host-agent calls `systemctl --user restart bloud-apps.target`
(the existing `ReloadAndRestartApps` path). Ordering is correct without host-agent orchestration
because the existing systemd dependency graph already handles it:

- Native system services (postgres, redis) expose user-scope readiness units: `systemd.user.paths`
  watching their sockets/ready-files (e.g. `/run/postgresql/.s.PGSQL.5432`).
- User-scope podman services declare `After=` and `Requires=` on those readiness units.
- Restarting `bloud-apps.target` lets systemd resolve the dependency graph — containers wait for
  native services to be ready before starting.

No additional host-agent orchestration is needed. The update flow calls `ReloadAndRestartApps`
after `nixos-rebuild switch` completes, same as the install path today.

### Postgres Socket Window

The path-unit readiness mechanism handles this correctly: the path unit watches for socket
existence and only activates its paired service unit (declaring success) once the socket is
present and accepting connections. Containers with `/run/postgresql` mounted won't restart until
that readiness signal fires.

---

## Post-Update Reconciliation

After `nixos-rebuild switch` and service restarts complete, the reconciler re-runs for all
installed apps:

- **PreStart** — re-applies directory creation and config file writes; idempotent
- **PostStart** — re-applies API integrations, OAuth blueprints, LDAP setup

This propagates any changes in `metadata.yaml` or `configurator.go` to already-running apps.

### Integration Config Refresh

Before reconciliation runs, the host-agent refreshes each app's integration config from
current `metadata.yaml`. This fixes the case where a Bloud update adds or changes an app's
declared integrations — the database's `integration_config` field is updated to match the
new metadata before PostStart runs.

---

## Rollback

### Automatic (Hard Failures)

Triggered when:
- `nixos-rebuild switch` exits non-zero
- A critical service (postgres, authentik) fails its health check within the timeout window

Action: `nixos-rebuild switch --rollback` + database sync from the previous generation's state.

### Database Consistency on Rollback

`apps-state.json` lives in the user data directory — it is not tracked by NixOS generations.
When an update writes a new `apps-state.json` via `generator.Apply()` and a rollback is later
triggered, `nixos-rebuild switch --rollback` reverts the Nix generation but leaves the new
`apps-state.json` on disk. The database then reflects the new state while the system is running
the old Nix config.

Fix: after `LoadCurrent()` is called and before `generator.Apply()` writes the new state,
snapshot the loaded app list to `apps-rollback-snapshot.json`. If rollback fires and
`LoadCurrent()` fails, or if the app set returned by `LoadCurrent()` differs from the app set
in the snapshot (apps present in one but not the other), restore database state from the
snapshot. The comparison is snapshot vs. current `apps-state.json` — not against `apps.nix`,
which `nixos-rebuild switch --rollback` does not revert (it lives in the user data directory,
outside NixOS generation tracking).

The existing `Orchestrator.Rollback()` already contains a partial database sync via
`generator.LoadCurrent()` → `o.graph.SetInstalled()`. This partial sync is incomplete (it
does not update `appStore` records or statuses) and must be replaced entirely by the snapshot
restore logic. The two code paths must not coexist.

### Manual (Soft Failures)

Available in the UI via the existing `/api/system/rollback` endpoint. The UI surfaces NixOS
generations so users can see the previous version and choose to revert.

---

## Error Surfacing

The existing reconciliation runs fire-and-forget goroutines and swallows PreStart errors
(logging only). For the update flow this is unacceptable — the user needs to know if something
went wrong.

The current `Reconcile()` in `reconcile.go` always returns `nil` — errors are collected in a
local slice that is never returned. The update path requires a modified signature (or a new
`ReconcileWithResults()` method) that returns per-app outcomes synchronously.

The update operation collects reconciliation results synchronously and returns a structured
response:

```json
{
  "status": "completed_with_warnings",
  "version": "2026.03.15",
  "apps": {
    "jellyfin":   { "status": "ok" },
    "authentik":  { "status": "ok" },
    "radarr":     { "status": "warning", "message": "PostStart: failed to connect to indexer" }
  }
}
```

`completed_with_warnings` means the system is running the new version but something needs
attention. The UI renders this as a yellow state with per-app detail.

---

## Pre-Flight Checks

Checked before any update action begins:

| Check | Failure behavior |
|-------|-----------------|
| Disk space ≥ 2 GB free | Block update, surface error in UI |
| Backup exists within last 7 days | Warn (not block) — user can override |
| No install/uninstall operation in progress | Block update until queue clears |
| Remote flake reachable (GitHub) | Block update with connectivity error |

The 7-day backup threshold and 2 GB disk floor are configurable in the host-agent config.

---

## API

### New endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/system/update/check` | Returns current version, latest available version, changelog summary |
| `POST` | `/api/system/update/apply` | Starts the update; streams progress via SSE |
| `GET`  | `/api/system/update/status` | Current update state (idle / running / completed / failed) |

`POST /api/system/update/apply` has its own SSE streaming implementation — it does NOT reuse
`/api/system/rebuild/stream`. The existing rebuild stream only covers `nixos-rebuild switch`
with no pre-flight checks, snapshot, image pulls, host-agent restart, or reconciliation. The
update endpoint streams the full multi-step flow and reconnect-safe status (see Host-Agent
Self-Restart above).

### Existing endpoints reused

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/system/versions` | NixOS generation list (used for rollback UI) |
| `POST` | `/api/system/rollback` | Roll back to previous generation |

---

## CI Pipeline Changes

### Release workflow additions

1. Build Go binary and frontend (existing)
2. Compute SHA256 of each artifact (new)
3. Write SHA256 + release URL into `nixos/packages/host-agent.nix` `fetchurl` entries (new)
4. Commit and push (to stable branch / tag) (new)
5. Build ISO (existing — ISO build still uses local artifacts)
6. Create GitHub release (existing)

### App version update workflow (new, separate)

A weekly scheduled workflow:
1. For each app in `apps/versions.json`, check registry for newer tags
2. If newer tag found, fetch its digest
3. Update `apps/versions.json` and relevant `module.nix` image strings
4. Open a PR titled "chore: bump app versions YYYY-MM-DD"

---

## State File Consistency

The current generator writes `apps.nix` and `apps-state.json` in two separate atomic renames.
A crash between them leaves state inconsistent.

Fix: write both files to temp paths first, then rename them in sequence. If the second rename
fails, the state file is stale but the Nix config is authoritative — on next startup,
`LoadCurrent()` re-derives state from the Nix config rather than failing.

The state file path is derived by string manipulation from the config path
(`strings.TrimSuffix(configPath, ".nix") + "-state.json"`). In practice `configPath` is a
constant so this is safe today, but the derivation should be replaced with a named constant to
make the coupling explicit and prevent accidental divergence.

---

## Out of Scope

- **Automatic background updates** — update trigger is always manual
- **Multiple update channels** — single stable channel only
- **Per-app version overrides** — app image versions are set per release, not per installation
- **Data migration tooling** — schema migrations are handled by each app on startup
- **Backup implementation** — backup system is a prerequisite; the pre-flight check warns if
  no backup exists but does not implement backups
- **Downgrade** — only rollback to the immediately previous NixOS generation is supported

---

## Required Before First Update Ships

**`nixosConfigurations.installed` flake attribute** — the current `flake.nix` exposes
`nixosConfigurations.bloud`. The update flow requires a `nixosConfigurations.installed`
attribute (same config, stable name independent of hostname). Three code changes required:
(1) add `nixosConfigurations.installed` to `flake.nix`; (2) add `nixosAttribute string` field
to `Rebuilder` (used instead of `r.hostname` when set); (3) add `DryRun` to `RebuilderInterface`
(currently only on the concrete struct). All three are required before any remote flake update
can function.

**`Reconcile()` return value** — `Reconcile()` currently always returns `nil`. The update
path needs per-app outcomes. A new `ReconcileWithResults()` method (or a modified signature)
that returns `map[string]ReconcileResult` is required.

**Per-app health check timeouts** — the `Reconciler.HealthCheck` phase uses a single shared
`r.config.HealthCheckTimeout` for all apps and does not consult the catalog. Authentik and
Miniflux run database migrations on startup and need longer timeouts.

The catalog model already has `HealthCheck.Timeout` (`timeout` in `metadata.yaml`), and
`waitForHealthy` in the orchestrator already reads it. The reconciler must be updated to read
`app.HealthCheck.Timeout` from the catalog per-app instead of using the shared
`ReconcileConfig.HealthCheckTimeout`. No new `metadata.yaml` field is needed — the existing
`timeout` field under `healthCheck` is the correct place:

```yaml
healthCheck:
  path: /health
  timeout: 300  # seconds; per-app override for reconciler health check wait
```

**Integration config refresh** — re-reading each installed app's `metadata.yaml` and updating
`integration_config` in the database before reconciliation is new behavior with no existing
code path. This requires a pre-reconciliation pass in the update handler.

---

## Open Questions

1. **Stable branch vs. tag**: Should `github:owner/bloud/stable` resolve to a branch (updated
   on each release) or a tag (immutable per release)? A branch is simpler to manage but a tag
   gives stronger reproducibility guarantees.

2. **Binary cache**: Without a binary cache, every in-place update re-evaluates and potentially
   re-downloads Nix closures. For systems with slow internet this could make updates take
   minutes. Worth adding a `cachix` or self-hosted cache later.

3. **Authentik API version compatibility window**: If a Bloud update bumps the Authentik
   container version, the old host-agent (still running during `nixos-rebuild switch`) will
   make API calls to a new Authentik container. The window is short but could cause SSO
   teardown/setup failures. Mitigation: cleanupSSO and blueprint application run after the new
   host-agent binary is active, not before.
