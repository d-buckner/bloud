# Automatic Updates Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement manual in-place system updates that atomically update NixOS config, host-agent binary, and all pinned container images via `nixos-rebuild switch --flake github:...#installed`.

**Architecture:** System updates pull from the remote flake (`github:owner/bloud/stable#installed`), which pins container image digests in `module.nix` and fetches pre-built host-agent artifacts via `fetchurl`. After `nixos-rebuild switch`, the host-agent restarts itself and resumes post-update steps (service restart, reconciliation) from a durable DB record.

**Tech Stack:** Go, NixOS/nixpkgs, rootless Podman, testify/mock, chi router, PostgreSQL

> **Phase note:** Chunk 1 (container image pinning) ships independently and provides immediate value. Chunks 2–9 are the full update mechanism. You can PR chunk 1 separately.

---

## File Structure

**New files:**
- `apps/versions.json` — image version manifest (source of truth for pinned tags + digests)
- `scripts/update-app-versions.sh` — fetches latest tags from registries, updates versions.json + module.nix
- `services/host-agent/internal/store/update_state.go` — UpdateStateStore (CRUD for system_update_state)
- `services/host-agent/internal/store/update_state_test.go`
- `services/host-agent/internal/orchestrator/update.go` — ApplyUpdate, runPreflightChecks, ResumeUpdate
- `services/host-agent/internal/orchestrator/update_test.go`
- `services/host-agent/internal/api/update.go` — handleUpdateCheck, handleUpdateApply, handleUpdateStatus
- `services/host-agent/internal/api/update_test.go`
- `.github/workflows/update-app-versions.yml` — weekly version bump PR workflow

**Modified files:**
- `apps/*/module.nix` (8 container apps) — image strings updated to digest-pinned form
- `flake.nix` — add `nixosConfigurations.installed`
- `nixos/packages/host-agent.nix` — add fetchurl fallback for remote flake evaluation
- `services/host-agent/internal/nixgen/interfaces.go` — add `DryRun` to RebuilderInterface
- `services/host-agent/internal/nixgen/rebuild.go` — add `nixosAttribute` field to Rebuilder
- `services/host-agent/internal/nixgen/rebuild_test.go` — tests for nixosAttribute override
- `services/host-agent/internal/orchestrator/interface.go` — add `ApplyUpdate` to AppOrchestrator
- `services/host-agent/internal/orchestrator/reconcile.go` — add ReconcileWithResults, per-app timeouts
- `services/host-agent/internal/orchestrator/reconcile_test.go` — new tests
- `services/host-agent/internal/orchestrator/orchestrator_nix.go` — rollback snapshot, ResumeUpdate
- `services/host-agent/internal/orchestrator/orchestrator_nix_test.go` — new tests
- `services/host-agent/internal/db/schema.sql` — add system_update_state table
- `services/host-agent/internal/api/routes.go` — register /api/system/update/* routes
- `services/host-agent/cmd/host-agent/main.go` — call ResumeUpdate after orchestrator init
- `.github/workflows/build-iso.yml` — SHA embedding steps

---

## Chunk 1: Container Image Pinning

### Task 1: Create versions.json manifest

**Files:**
- Create: `apps/versions.json`
- Create: `scripts/update-app-versions.sh`

- [ ] **Step 1: Create initial versions.json with placeholder digests**

```json
{
  "actual-budget": {
    "image": "actualbudget/actual-server",
    "tag": "25.1.0",
    "digest": "sha256:PLACEHOLDER"
  },
  "jellyfin": {
    "image": "jellyfin/jellyfin",
    "tag": "10.10.6",
    "digest": "sha256:PLACEHOLDER"
  },
  "jellyseerr": {
    "image": "fallenbagel/jellyseerr",
    "tag": "2.3.0",
    "digest": "sha256:PLACEHOLDER"
  },
  "miniflux": {
    "image": "miniflux/miniflux",
    "tag": "2.2.5",
    "digest": "sha256:PLACEHOLDER"
  },
  "prowlarr": {
    "image": "linuxserver/prowlarr",
    "tag": "1.30.2",
    "digest": "sha256:PLACEHOLDER"
  },
  "radarr": {
    "image": "linuxserver/radarr",
    "tag": "5.16.3",
    "digest": "sha256:PLACEHOLDER"
  },
  "sonarr": {
    "image": "linuxserver/sonarr",
    "tag": "4.0.13",
    "digest": "sha256:PLACEHOLDER"
  },
  "qbittorrent": {
    "image": "linuxserver/qbittorrent",
    "tag": "5.0.4",
    "digest": "sha256:PLACEHOLDER"
  },
  "flood": {
    "image": "jesec/flood",
    "tag": "master",
    "digest": "sha256:PLACEHOLDER"
  }
}
```

- [ ] **Step 2: Create `scripts/update-app-versions.sh`**

```bash
#!/usr/bin/env bash
# Fetches current image digests from Docker Hub/GHCR for each app in versions.json.
# Updates versions.json and regenerates `image =` lines in each app's module.nix.
#
# Usage: ./scripts/update-app-versions.sh
# Requires: docker (or podman), jq

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
VERSIONS_FILE="$ROOT_DIR/apps/versions.json"

if ! command -v jq &>/dev/null; then
  echo "Error: jq is required" >&2; exit 1
fi

PULLER="${DOCKER_CMD:-docker}"

apps=$(jq -r 'keys[]' "$VERSIONS_FILE")

for app in $apps; do
  image=$(jq -r --arg app "$app" '.[$app].image' "$VERSIONS_FILE")
  tag=$(jq -r --arg app "$app" '.[$app].tag' "$VERSIONS_FILE")
  full="${image}:${tag}"

  echo "Pulling ${full}..."
  $PULLER pull --quiet "$full" >/dev/null

  digest=$($PULLER inspect --format='{{index .RepoDigests 0}}' "$full" 2>/dev/null \
    | sed 's/.*@//')

  if [ -z "$digest" ]; then
    echo "  Warning: could not get digest for $full, skipping" >&2
    continue
  fi

  echo "  digest: $digest"

  # Update versions.json
  tmp=$(mktemp)
  jq --arg app "$app" --arg digest "$digest" \
    '.[$app].digest = $digest' "$VERSIONS_FILE" > "$tmp"
  mv "$tmp" "$VERSIONS_FILE"

  # Rewrite image = "..." line in module.nix
  module_nix="$ROOT_DIR/apps/$app/module.nix"
  if [ -f "$module_nix" ]; then
    pinned="${image}:${tag}@${digest}"
    # Replace any existing image = "..." that contains this image name
    sed -i "s|image = \"${image}:[^\"]*\"|image = \"${pinned}\"|g" "$module_nix"
    echo "  updated $module_nix"
  fi
done

echo "Done. Review changes with: git diff apps/"
```

- [ ] **Step 3: Make script executable**

```bash
chmod +x /Users/daniel/Projects/bloud/scripts/update-app-versions.sh
```

- [ ] **Step 4: Run the script to populate real digests**

```bash
cd /Users/daniel/Projects/bloud && ./scripts/update-app-versions.sh
```

Expected: versions.json updated with real sha256 digests, each `module.nix` updated.

- [ ] **Step 5: Verify all digests populated**

```bash
jq '.[] | select(.digest == "sha256:PLACEHOLDER")' apps/versions.json
```

Expected: empty output (no PLACEHOLDERs remaining).

- [ ] **Step 6: Commit**

```bash
git add apps/versions.json scripts/update-app-versions.sh
git commit -m "feat: add container image version manifest and update script"
```

---

### Task 2: Verify module.nix files are pinned

**Files:**
- Modify: `apps/*/module.nix` (updated by script in Task 1)

- [ ] **Step 1: Check all `:latest` tags are gone from container apps**

```bash
grep -rn "image.*:latest" /Users/daniel/Projects/bloud/apps/
```

Expected: no output (no `:latest` remaining in container apps).

- [ ] **Step 2: Check each module.nix has `@sha256:` in image string**

```bash
grep -l "image = " /Users/daniel/Projects/bloud/apps/*/module.nix \
  | xargs grep -L "@sha256:"
```

Expected: empty output (all image lines have a digest). If any files appear, edit them manually to add the digest from versions.json.

- [ ] **Step 3: Commit pinned module.nix files**

```bash
git add apps/*/module.nix
git commit -m "feat: pin container image versions to digests"
```

---

## Chunk 2: Rebuilder Prerequisites

### Task 3: Add `DryRun` to `RebuilderInterface`

**Files:**
- Modify: `services/host-agent/internal/nixgen/interfaces.go`
- Modify: `services/host-agent/internal/nixgen/rebuild_test.go`

- [ ] **Step 1: Add `DryRun` to the interface in `interfaces.go`**

In `interfaces.go`, add `DryRun` to `RebuilderInterface`:

```go
type RebuilderInterface interface {
    Switch(ctx context.Context) (*RebuildResult, error)
    Rollback(ctx context.Context) (*RebuildResult, error)
    SwitchStream(ctx context.Context, events chan<- RebuildEvent)
    StopUserService(ctx context.Context, appName string) error
    ReloadAndRestartApps(ctx context.Context) error
    DryRun(ctx context.Context) (*RebuildResult, error)  // added
}
```

- [ ] **Step 2: Add `DryRun` to `MockRebuilder` in `orchestrator/mocks_test.go`**

`MockRebuilder` must satisfy the updated `RebuilderInterface`. Add to `mocks_test.go`:

```go
func (m *MockRebuilder) DryRun(ctx context.Context) (*nixgen.RebuildResult, error) {
    args := m.Called(ctx)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(*nixgen.RebuildResult), args.Error(1)
}
```

- [ ] **Step 3: Run full test suite to confirm nothing broken**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add services/host-agent/internal/nixgen/interfaces.go \
        services/host-agent/internal/orchestrator/mocks_test.go
git commit -m "feat: add DryRun to RebuilderInterface"
```

---

### Task 4: Add `nixosAttribute` field to `Rebuilder`

**Files:**
- Modify: `services/host-agent/internal/nixgen/rebuild.go`
- Modify: `services/host-agent/internal/nixgen/rebuild_test.go`

- [ ] **Step 1: Write failing test**

In `rebuild_test.go`, add:

```go
func TestRebuilderUsesNixosAttributeWhenSet(t *testing.T) {
    r := &Rebuilder{
        flakePath:      "/tmp/test-flake",
        hostname:       "myhost",
        nixosAttribute: "installed",
        logger:         slog.Default(),
        impure:         true,
        useSudo:        false,
    }

    // flakeURI() prepends "path:" to absolute paths, so /tmp/test-flake → path:/tmp/test-flake
    flakeArg := fmt.Sprintf("%s#%s", r.flakeURI(), r.nixosAttributeOrHostname())
    assert.Equal(t, "path:/tmp/test-flake#installed", flakeArg)
}

func TestRebuilderFallsBackToHostnameWhenAttributeEmpty(t *testing.T) {
    r := &Rebuilder{
        flakePath: "/tmp/test-flake",
        hostname:  "myhost",
        logger:    slog.Default(),
    }
    flakeArg := fmt.Sprintf("%s#%s", r.flakeURI(), r.nixosAttributeOrHostname())
    assert.Equal(t, "path:/tmp/test-flake#myhost", flakeArg)
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/nixgen/... -run "TestRebuilderUses|TestRebuilderFalls" -v
```

Expected: compile error — `nixosAttribute` field and `nixosAttributeOrHostname()` don't exist yet.

- [ ] **Step 3: Add `nixosAttribute` field and helper to `rebuild.go`**

Add only the `nixosAttribute string` field to the existing `Rebuilder` struct (all other fields already exist — do not duplicate them):
```go
// In the Rebuilder struct, add:
nixosAttribute string  // when set, overrides hostname for the flake attribute
```

Add helper method:
```go
func (r *Rebuilder) nixosAttributeOrHostname() string {
    if r.nixosAttribute != "" {
        return r.nixosAttribute
    }
    return r.hostname
}
```

Update the `--flake` argument in `Switch()` (find the line `fmt.Sprintf("%s#%s", r.flakeURI(), r.hostname)` and change it):
```go
args = append(args, "--flake", fmt.Sprintf("%s#%s", r.flakeURI(), r.nixosAttributeOrHostname()))
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/nixgen/... -run "TestRebuilderUses|TestRebuilderFalls" -v
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add services/host-agent/internal/nixgen/rebuild.go \
        services/host-agent/internal/nixgen/rebuild_test.go
git commit -m "feat: add nixosAttribute override to Rebuilder"
```

---

### Task 5: Add `nixosConfigurations.installed` to `flake.nix`

**Files:**
- Modify: `flake.nix`

- [ ] **Step 1: Add `installed` configuration to flake.nix**

In `flake.nix`, after the `bloud = nixpkgs.lib.nixosSystem { ... };` block, add:

```nix
# Stable attribute for in-place updates via remote flake.
# nixos-rebuild switch --flake github:owner/bloud/stable#installed
# (identical to `bloud`; `bloud` is kept for installer compatibility)
installed = nixpkgs.lib.nixosSystem {
  system = "x86_64-linux";
  modules = [
    ./nixos/installed.nix
    ./nixos/bloud.nix
  ];
};
```

- [ ] **Step 2: Verify the flake evaluates**

```bash
cd /Users/daniel/Projects/bloud && \
  nix eval .#nixosConfigurations.installed.config.system.build.toplevel --no-build 2>&1 | head -5
```

Expected: outputs a store path like `/nix/store/HASH-nixos-system-...` (not an error).

- [ ] **Step 3: Commit**

```bash
git add flake.nix
git commit -m "feat: add nixosConfigurations.installed for remote update target"
```

---

## Chunk 3: Reconciler Improvements

### Task 6: Add `ReconcileWithResults()`

**Files:**
- Modify: `services/host-agent/internal/orchestrator/reconcile.go`
- Modify: `services/host-agent/internal/orchestrator/reconcile_test.go`

- [ ] **Step 1: Write failing test**

In `reconcile_test.go`, add:

```go
func TestReconcileWithResultsReturnsPerAppOutcomes(t *testing.T) {
    tr := newTestReconciler()

    installedApps := []*store.InstalledApp{
        {Name: "jellyfin", Status: "running", IntegrationConfig: map[string]string{}},
        {Name: "radarr",   Status: "running", IntegrationConfig: map[string]string{}},
    }
    tr.appStore.On("GetAll").Return(installedApps, nil)

    jellyfinCfg := new(MockConfigurator)
    radarrCfg := new(MockConfigurator)

    // HealthCheck takes only ctx (not state). Use single mock.Anything matcher.
    jellyfinCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
    jellyfinCfg.On("HealthCheck", mock.Anything).Return(nil)
    jellyfinCfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

    radarrCfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
    radarrCfg.On("HealthCheck", mock.Anything).Return(nil)
    radarrCfg.On("PostStart", mock.Anything, mock.Anything).Return(
        errors.New("indexer unreachable"),
    )

    // registry.Get returns a single value (configurator.Configurator), not (Configurator, error)
    tr.registry.On("Get", "jellyfin").Return(jellyfinCfg)
    tr.registry.On("Get", "radarr").Return(radarrCfg)

    results, err := tr.reconciler.ReconcileWithResults(context.Background())
    require.NoError(t, err)

    assert.Equal(t, "ok", results["jellyfin"].Status)
    assert.Equal(t, "warning", results["radarr"].Status)
    assert.Contains(t, results["radarr"].Message, "indexer unreachable")
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestReconcileWithResultsReturnsPerAppOutcomes -v
```

Expected: compile error — `ReconcileWithResults` doesn't exist yet.

- [ ] **Step 3: Add `AppReconcileResult` type and `ReconcileWithResults()` to `reconcile.go`**

Add after existing types:
```go
// AppReconcileResult holds the outcome of reconciling a single app.
type AppReconcileResult struct {
    Status  string // "ok" | "warning"
    Message string
}
```

Add method (mirrors `Reconcile()` but collects and returns per-app results):
```go
// ReconcileWithResults runs the full reconciliation cycle and returns per-app outcomes.
// Unlike Reconcile(), it does not swallow errors — all phase failures are surfaced.
func (r *Reconciler) ReconcileWithResults(ctx context.Context) (map[string]AppReconcileResult, error) {
    apps, err := r.appStore.GetAll()
    if err != nil {
        return nil, fmt.Errorf("get apps: %w", err)
    }

    results := make(map[string]AppReconcileResult, len(apps))
    appMap := make(map[string]*store.InstalledApp, len(apps))
    for _, app := range apps {
        appMap[app.Name] = app
        results[app.Name] = AppReconcileResult{Status: "ok"}
    }

    // Filter out apps mid-uninstall (mirrors existing Reconcile() behaviour)
    for name := range appMap {
        if appMap[name].Status == "uninstalling" {
            delete(appMap, name)
            delete(results, name)
        }
    }

    levels := r.computeLevels(appMap)

    for _, level := range levels {
        for _, appName := range level {
            app := appMap[appName]
            state := r.buildAppState(app)

            // registry.Get returns a single value (configurator.Configurator), not an error
            cfg := r.registry.Get(appName)
            if cfg == nil {
                results[appName] = AppReconcileResult{
                    Status:  "warning",
                    Message: "no configurator registered",
                }
                continue
            }

            if err := cfg.PreStart(ctx, state); err != nil {
                results[appName] = AppReconcileResult{
                    Status:  "warning",
                    Message: fmt.Sprintf("PreStart: %v", err),
                }
                continue
            }

            // HealthCheck takes only ctx (no state argument)
            if err := cfg.HealthCheck(ctx); err != nil {
                results[appName] = AppReconcileResult{
                    Status:  "warning",
                    Message: fmt.Sprintf("HealthCheck: %v", err),
                }
                continue
            }

            if err := cfg.PostStart(ctx, state); err != nil {
                results[appName] = AppReconcileResult{
                    Status:  "warning",
                    Message: fmt.Sprintf("PostStart: %v", err),
                }
            }
        }
    }

    return results, nil
}
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestReconcileWithResultsReturnsPerAppOutcomes -v
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add services/host-agent/internal/orchestrator/reconcile.go \
        services/host-agent/internal/orchestrator/reconcile_test.go
git commit -m "feat: add ReconcileWithResults for per-app update outcomes"
```

---

### Task 7: Per-app health check timeout from catalog

**Files:**
- Modify: `services/host-agent/internal/orchestrator/reconcile.go`
- Modify: `services/host-agent/internal/orchestrator/reconcile_test.go`

The reconciler's `HealthCheck` phase currently uses `r.config.HealthCheckTimeout` (shared for all apps). It must read `app.HealthCheck.Timeout` from the catalog per-app. The `Reconciler` already has a `catalogCache` field for this purpose.

- [ ] **Step 1: Write failing test**

```go
func TestReconcileUsesPerAppHealthCheckTimeout(t *testing.T) {
    tr := newTestReconciler()

    installedApps := []*store.InstalledApp{
        {Name: "authentik", Status: "running", IntegrationConfig: map[string]string{}},
    }
    tr.appStore.On("GetAll").Return(installedApps, nil)

    // Catalog returns 300s timeout for authentik
    authentikMeta := &catalog.App{
        Name: "authentik",
        HealthCheck: catalog.HealthCheck{
            Path:    "/api/v3/root/config/",
            Timeout: 300,
        },
    }
    tr.reconciler.catalogCache = new(MockCatalogCache)
    tr.reconciler.catalogCache.(*MockCatalogCache).On("Get", "authentik").Return(authentikMeta, nil)

    cfg := new(MockConfigurator)
    cfg.On("PreStart", mock.Anything, mock.Anything).Return(nil)
    // HealthCheck takes only ctx (no state argument)
    cfg.On("HealthCheck", mock.Anything).
        Run(func(args mock.Arguments) {
            ctx := args.Get(0).(context.Context)
            deadline, ok := ctx.Deadline()
            require.True(t, ok, "context should have deadline")
            remaining := time.Until(deadline)
            // Should be ~300s, not the default 60s
            assert.Greater(t, remaining, 200*time.Second)
        }).
        Return(nil)
    cfg.On("PostStart", mock.Anything, mock.Anything).Return(nil)

    // registry.Get returns a single value (configurator.Configurator), not (Configurator, error)
    tr.registry.On("Get", "authentik").Return(cfg)

    _, err := tr.reconciler.ReconcileWithResults(context.Background())
    require.NoError(t, err)
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestReconcileUsesPerAppHealthCheckTimeout -v
```

Expected: FAIL — context deadline is the shared timeout, not 300s.

- [ ] **Step 3: Update `ReconcileWithResults()` to read per-app timeout**

In the `ReconcileWithResults` HealthCheck section, replace the plain `cfg.HealthCheck(hcCtx)` call with a timeout-bounded context:

```go
// Determine health check timeout: per-app catalog value takes precedence.
timeout := r.config.HealthCheckTimeout
if r.catalogCache != nil {
    if meta, err := r.catalogCache.Get(appName); err == nil && meta.HealthCheck.Timeout > 0 {
        timeout = time.Duration(meta.HealthCheck.Timeout) * time.Second
    }
}

hcCtx, cancel := context.WithTimeout(ctx, timeout)
hcErr := cfg.HealthCheck(hcCtx)  // HealthCheck takes only ctx (no state argument)
cancel()
if hcErr != nil {
    results[appName] = AppReconcileResult{
        Status:  "warning",
        Message: fmt.Sprintf("HealthCheck: %v", hcErr),
    }
    continue
}
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestReconcileUsesPerAppHealthCheckTimeout -v
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 6: Commit**

```bash
git add services/host-agent/internal/orchestrator/reconcile.go \
        services/host-agent/internal/orchestrator/reconcile_test.go
git commit -m "feat: reconciler reads per-app health check timeout from catalog"
```

---

## Chunk 4: Rollback Snapshot

### Task 8: Write rollback snapshot before `generator.Apply()`

**Files:**
- Modify: `services/host-agent/internal/orchestrator/orchestrator_nix.go`
- Modify: `services/host-agent/internal/orchestrator/orchestrator_nix_test.go`

The snapshot must be written after `LoadCurrent()` and before `generator.Apply()`. It will be read during rollback to restore state if `LoadCurrent()` fails post-rollback.

- [ ] **Step 1: Write failing test**

```go
func TestRollbackSnapshotWrittenBeforeApply(t *testing.T) {
    to := newTestOrchestratorWithMocks()

    current := &nixgen.Transaction{
        Apps: map[string]nixgen.AppConfig{
            "jellyfin": {Name: "jellyfin", Enabled: true},
        },
    }
    to.generator.On("LoadCurrent").Return(current, nil)
    to.generator.On("Apply", mock.Anything).Return(nil)
    // ... other mocks as needed for a minimal Install

    snapshotPath := filepath.Join(to.orch.dataDir, "nix", "apps-rollback-snapshot.json")

    // Trigger an Install which calls LoadCurrent + Apply
    to.appStore.On("Install", mock.Anything, mock.Anything, mock.Anything,
        mock.Anything, mock.Anything).Return(nil)
    to.appStore.On("GetAll").Return([]*store.InstalledApp{}, nil)
    to.rebuilder.On("Switch", mock.Anything).Return(&nixgen.RebuildResult{Success: true}, nil)
    to.rebuilder.On("ReloadAndRestartApps", mock.Anything).Return(nil)
    // ... add remaining mocks as required by Install

    to.orch.EnqueueInstall(context.Background(), orchestrator.InstallRequest{App: "jellyfin"})

    // Snapshot must exist after Apply
    _, err := os.Stat(snapshotPath)
    assert.NoError(t, err, "rollback snapshot should be written before Apply")

    // Snapshot content must match current state
    data, _ := os.ReadFile(snapshotPath)
    var snap nixgen.Transaction
    require.NoError(t, json.Unmarshal(data, &snap))
    assert.Contains(t, snap.Apps, "jellyfin")
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestRollbackSnapshotWrittenBeforeApply -v
```

Expected: FAIL — snapshot file does not exist.

- [ ] **Step 3: Add `saveRollbackSnapshot()` helper and call it in `enqueueWork()`**

In `orchestrator_nix.go`, add a private helper:

```go
func (o *Orchestrator) saveRollbackSnapshot(tx *nixgen.Transaction) error {
    snapshotPath := filepath.Join(o.dataDir, "nix", "apps-rollback-snapshot.json")
    data, err := json.Marshal(tx)
    if err != nil {
        return fmt.Errorf("marshal rollback snapshot: %w", err)
    }
    tmp := snapshotPath + ".tmp"
    if err := os.WriteFile(tmp, data, 0644); err != nil {
        return fmt.Errorf("write rollback snapshot: %w", err)
    }
    return os.Rename(tmp, snapshotPath)
}
```

In the install/uninstall flow, immediately before the `generator.Apply(tx)` call, add:

```go
if err := o.saveRollbackSnapshot(current); err != nil {
    o.logger.Warn("failed to save rollback snapshot", "error", err)
    // non-fatal: continue without snapshot
}
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestRollbackSnapshotWrittenBeforeApply -v
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 6: Commit**

```bash
git add services/host-agent/internal/orchestrator/orchestrator_nix.go \
        services/host-agent/internal/orchestrator/orchestrator_nix_test.go
git commit -m "feat: write rollback snapshot before generator.Apply()"
```

---

### Task 9: Replace rollback DB sync with snapshot restore

**Files:**
- Modify: `services/host-agent/internal/orchestrator/orchestrator_nix.go`
- Modify: `services/host-agent/internal/orchestrator/orchestrator_nix_test.go`

The existing `Rollback()` has a partial database sync via `LoadCurrent()` → `o.graph.SetInstalled()` that doesn't update `appStore` records. Replace it entirely with snapshot-based restore.

- [ ] **Step 1: Write failing test**

```go
func TestRollbackRestoresFromSnapshotWhenLoadCurrentFails(t *testing.T) {
    to := newTestOrchestratorWithMocks()

    // Write a snapshot with known state
    snapshotApps := map[string]nixgen.AppConfig{
        "jellyfin": {Name: "jellyfin", Enabled: true},
    }
    snapshot := &nixgen.Transaction{Apps: snapshotApps}
    snapshotPath := filepath.Join(to.orch.dataDir, "nix", "apps-rollback-snapshot.json")
    require.NoError(t, os.MkdirAll(filepath.Dir(snapshotPath), 0755))
    data, _ := json.Marshal(snapshot)
    require.NoError(t, os.WriteFile(snapshotPath, data, 0644))

    // LoadCurrent fails (simulates post-rollback state file mismatch)
    to.generator.On("LoadCurrent").Return(nil, errors.New("state file mismatch"))
    to.rebuilder.On("Rollback", mock.Anything).Return(&nixgen.RebuildResult{Success: true}, nil)

    // appStore.UpdateStatus should be called to restore apps
    to.appStore.On("UpdateStatus", "jellyfin", "running").Return(nil)
    to.graph.On("SetInstalled", []string{"jellyfin"}).Return(nil)

    _, err := to.orch.Rollback(context.Background())
    require.NoError(t, err)

    to.appStore.AssertCalled(t, "UpdateStatus", "jellyfin", "running")
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestRollbackRestoresFromSnapshot -v
```

Expected: FAIL — snapshot restore logic not implemented.

- [ ] **Step 3: Rewrite rollback DB sync in `Rollback()`**

In `orchestrator_nix.go`, find `Rollback()`. Replace the existing post-rollback sync block with:

```go
// Attempt to restore app state from snapshot; fall back to LoadCurrent if no snapshot.
if err := o.restoreFromSnapshot(); err != nil {
    o.logger.Warn("snapshot restore failed, attempting LoadCurrent", "error", err)
    if cur, lcErr := o.generator.LoadCurrent(); lcErr == nil {
        names := make([]string, 0, len(cur.Apps))
        for name := range cur.Apps {
            names = append(names, name)
        }
        _ = o.graph.SetInstalled(names)
    }
}
```

Add `restoreFromSnapshot()`:

```go
func (o *Orchestrator) restoreFromSnapshot() error {
    snapshotPath := filepath.Join(o.dataDir, "nix", "apps-rollback-snapshot.json")
    data, err := os.ReadFile(snapshotPath)
    if err != nil {
        return fmt.Errorf("read snapshot: %w", err)
    }
    var snap nixgen.Transaction
    if err := json.Unmarshal(data, &snap); err != nil {
        return fmt.Errorf("unmarshal snapshot: %w", err)
    }

    names := make([]string, 0, len(snap.Apps))
    for name := range snap.Apps {
        names = append(names, name)
        _ = o.appStore.UpdateStatus(name, "running")
    }
    return o.graph.SetInstalled(names)
}
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run TestRollbackRestoresFromSnapshot -v
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 6: Commit**

```bash
git add services/host-agent/internal/orchestrator/orchestrator_nix.go \
        services/host-agent/internal/orchestrator/orchestrator_nix_test.go
git commit -m "feat: restore from rollback snapshot instead of partial LoadCurrent sync"
```

---

## Chunk 5: Update State Schema + Store

### Task 10: Add `system_update_state` table

**Files:**
- Modify: `services/host-agent/internal/db/schema.sql`

- [ ] **Step 1: Add table to schema.sql**

Append to `schema.sql`:

```sql
-- Durable update-in-progress state. Single-row table (id must be 1).
-- The host-agent writes this before self-restarting so the new binary
-- can resume from the correct step.
CREATE TABLE IF NOT EXISTS system_update_state (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    status     TEXT NOT NULL,   -- 'in_progress' | 'completed' | 'failed'
    step       TEXT NOT NULL,   -- last completed step: 'preflight' | 'dryrun' | 'pull' | 'rebuild' | 'restart' | 'reconcile'
    version    TEXT NOT NULL,
    flake_ref  TEXT NOT NULL,
    error      TEXT,
    started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- [ ] **Step 2: Verify schema parses**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/db/... -v
```

Expected: PASS (db tests apply schema; new table is created without error).

- [ ] **Step 3: Commit**

```bash
git add services/host-agent/internal/db/schema.sql
git commit -m "feat: add system_update_state table for durable update tracking"
```

---

### Task 11: Create `UpdateStateStore`

**Files:**
- Modify: `services/host-agent/internal/testdb/testdb.go` — add `system_update_state` to Schema
- Create: `services/host-agent/internal/store/update_state.go`
- Create: `services/host-agent/internal/store/update_state_test.go`

- [ ] **Step 1: Add `system_update_state` to `testdb.Schema`**

In `testdb/testdb.go`, append the new table to the `Schema` constant (after `catalog_cache`):

```go
CREATE TABLE IF NOT EXISTS system_update_state (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    status     TEXT NOT NULL,
    step       TEXT NOT NULL,
    version    TEXT NOT NULL,
    flake_ref  TEXT NOT NULL,
    error      TEXT,
    started_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Also add `"system_update_state"` to the `cleanupTables` slice in `SetupTestDB`:
```go
cleanupTables := []string{"apps", "catalog_cache", "system_update_state"}
```

- [ ] **Step 2: Write failing test**

Create `store/update_state_test.go`:

```go
package store_test

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
    "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/testdb"
)

func TestUpdateStateSaveAndLoad(t *testing.T) {
    db := testdb.SetupTestDB(t)
    s := store.NewUpdateStateStore(db)

    err := s.Save(store.UpdateState{
        Status:   "in_progress",
        Step:     "rebuild",
        Version:  "2026.03.15",
        FlakeRef: "github:owner/bloud/stable",
    })
    require.NoError(t, err)

    state, err := s.Load()
    require.NoError(t, err)
    assert.Equal(t, "in_progress", state.Status)
    assert.Equal(t, "rebuild", state.Step)
    assert.Equal(t, "2026.03.15", state.Version)
}

func TestUpdateStateLoadReturnsNilWhenEmpty(t *testing.T) {
    db := testdb.SetupTestDB(t)
    s := store.NewUpdateStateStore(db)

    state, err := s.Load()
    require.NoError(t, err)
    assert.Nil(t, state)
}

func TestUpdateStateComplete(t *testing.T) {
    db := testdb.SetupTestDB(t)
    s := store.NewUpdateStateStore(db)

    require.NoError(t, s.Save(store.UpdateState{
        Status: "in_progress", Step: "rebuild",
        Version: "2026.03.15", FlakeRef: "github:owner/bloud/stable",
    }))
    require.NoError(t, s.Complete())

    state, err := s.Load()
    require.NoError(t, err)
    assert.Equal(t, "completed", state.Status)
}
```

- [ ] **Step 3: Run test to confirm it fails**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/store/... -run "TestUpdateState" -v
```

Expected: compile error — `UpdateStateStore` doesn't exist yet.

- [ ] **Step 4: Implement `store/update_state.go`**

```go
package store

import (
    "database/sql"
    "time"
)

type UpdateState struct {
    Status   string
    Step     string
    Version  string
    FlakeRef string
    Error    string
    StartedAt time.Time
    UpdatedAt time.Time
}

type UpdateStateStore struct {
    db *sql.DB
}

func NewUpdateStateStore(db *sql.DB) *UpdateStateStore {
    return &UpdateStateStore{db: db}
}

// Save upserts the single update-state row.
func (s *UpdateStateStore) Save(state UpdateState) error {
    _, err := s.db.Exec(`
        INSERT INTO system_update_state (id, status, step, version, flake_ref, error, updated_at)
        VALUES (1, $1, $2, $3, $4, $5, CURRENT_TIMESTAMP)
        ON CONFLICT (id) DO UPDATE SET
            status     = excluded.status,
            step       = excluded.step,
            version    = excluded.version,
            flake_ref  = excluded.flake_ref,
            error      = excluded.error,
            updated_at = CURRENT_TIMESTAMP`,
        state.Status, state.Step, state.Version, state.FlakeRef, state.Error)
    return err
}

// Advance updates the step field and timestamp.
func (s *UpdateStateStore) Advance(step string) error {
    _, err := s.db.Exec(`
        UPDATE system_update_state
        SET step = $1, updated_at = CURRENT_TIMESTAMP
        WHERE id = 1`, step)
    return err
}

// Complete marks the update as completed.
func (s *UpdateStateStore) Complete() error {
    _, err := s.db.Exec(`
        UPDATE system_update_state
        SET status = 'completed', updated_at = CURRENT_TIMESTAMP
        WHERE id = 1`)
    return err
}

// Fail marks the update as failed with an error message.
func (s *UpdateStateStore) Fail(msg string) error {
    _, err := s.db.Exec(`
        UPDATE system_update_state
        SET status = 'failed', error = $1, updated_at = CURRENT_TIMESTAMP
        WHERE id = 1`, msg)
    return err
}

// Load returns the current update state, or nil if no row exists.
func (s *UpdateStateStore) Load() (*UpdateState, error) {
    row := s.db.QueryRow(`
        SELECT status, step, version, flake_ref, COALESCE(error,''), started_at, updated_at
        FROM system_update_state WHERE id = 1`)
    var st UpdateState
    err := row.Scan(&st.Status, &st.Step, &st.Version, &st.FlakeRef,
        &st.Error, &st.StartedAt, &st.UpdatedAt)
    if err == sql.ErrNoRows {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }
    return &st, nil
}
```

- [ ] **Step 5: Run tests to confirm they pass**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/store/... -run "TestUpdateState" -v
```

Expected: PASS.

- [ ] **Step 6: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 7: Commit**

```bash
git add services/host-agent/internal/testdb/testdb.go \
        services/host-agent/internal/store/update_state.go \
        services/host-agent/internal/store/update_state_test.go
git commit -m "feat: add UpdateStateStore for durable update tracking"
```

---

## Chunk 6: Update Orchestration

### Task 12: Add `ApplyUpdate` to `AppOrchestrator` interface

**Files:**
- Modify: `services/host-agent/internal/orchestrator/interface.go`

- [ ] **Step 1: Add types and method to `interface.go`**

```go
// UpdateRequest initiates a system update from a remote flake.
type UpdateRequest struct {
    // FlakeRef is the Nix flake reference, e.g. "github:owner/bloud/stable".
    FlakeRef string
}

// AppUpdateResult holds the per-app outcome of post-update reconciliation.
type AppUpdateResult struct {
    Status  string // "ok" | "warning"
    Message string
}

// UpdateResult is the final outcome of ApplyUpdate.
type UpdateResult struct {
    Version string
    // Status is "completed" or "completed_with_warnings".
    Status string
    Apps   map[string]AppUpdateResult
}

type AppOrchestrator interface {
    Install(ctx context.Context, req InstallRequest) (InstallResponse, error)
    Uninstall(ctx context.Context, req UninstallRequest) (UninstallResponse, error)
    RegenerateRoutes() error
    // ApplyUpdate applies a system update from the given flake ref.
    // It runs preflight checks, pulls images, rebuilds, restarts services,
    // and reconciles all installed apps. The host-agent restarts itself
    // mid-update; the caller should poll /api/system/update/status to track
    // progress after the connection drops.
    ApplyUpdate(ctx context.Context, req UpdateRequest) (*UpdateResult, error)
}
```

- [ ] **Step 2: Run full test suite to catch any compile errors**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

Expected: compile errors in any mock that implements `AppOrchestrator` — add a stub `ApplyUpdate` to each.

- [ ] **Step 3: Add stub `ApplyUpdate` to test mocks**

Find all types implementing `AppOrchestrator` (grep for `AppOrchestrator` in test files) and add:

```go
func (m *MockOrchestrator) ApplyUpdate(ctx context.Context, req orchestrator.UpdateRequest) (*orchestrator.UpdateResult, error) {
    args := m.Called(ctx, req)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(*orchestrator.UpdateResult), args.Error(1)
}
```

- [ ] **Step 4: Run full test suite to confirm all PASS**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 5: Commit**

```bash
git add services/host-agent/internal/orchestrator/interface.go
git commit -m "feat: add ApplyUpdate to AppOrchestrator interface"
```

---

### Task 13: Implement preflight checks and `ApplyUpdate`

**Files:**
- Create: `services/host-agent/internal/orchestrator/update.go`
- Create: `services/host-agent/internal/orchestrator/update_test.go`

- [ ] **Step 1: Write failing tests for preflight**

Create `update_test.go`:

> **Note:** This file MUST be `package orchestrator` (not `package orchestrator_test`) because the tests
> access unexported fields (`diskChecker`) and methods (`runPreflightChecks`). Match the
> package declaration in `orchestrator_nix_test.go`.

```go
package orchestrator

import (
    "context"
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestPreflightFailsOnLowDisk(t *testing.T) {
    to := newTestOrchestratorWithMocks()
    // Inject a disk checker that reports low space
    to.orch.diskChecker = func() (int64, error) { return 500 * 1024 * 1024, nil } // 500 MB

    result, err := to.orch.runPreflightChecks(context.Background(), "github:owner/bloud/stable")
    require.NoError(t, err)
    assert.False(t, result.DiskSpaceOK)
}

func TestPreflightPassesOnSufficientDisk(t *testing.T) {
    to := newTestOrchestratorWithMocks()
    to.orch.diskChecker = func() (int64, error) { return 5 * 1024 * 1024 * 1024, nil } // 5 GB

    result, err := to.orch.runPreflightChecks(context.Background(), "github:owner/bloud/stable")
    require.NoError(t, err)
    assert.True(t, result.DiskSpaceOK)
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run "TestPreflight" -v
```

Expected: compile error.

- [ ] **Step 3: Implement `update.go`**

Create `orchestrator/update.go`:

```go
package orchestrator

import (
    "context"
    "fmt"
    "strings"
    "syscall"
    "time"

    "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/nixgen"
    "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

const minDiskBytes = 2 * 1024 * 1024 * 1024 // 2 GB
const backupWarnDays = 7

// PreflightResult holds the result of pre-update checks.
type PreflightResult struct {
    DiskSpaceOK   bool
    DiskSpaceFree int64
    BackupOK      bool
    BackupAge     *time.Duration
    // QueueEmpty is always true for now; TODO: check for in-progress operations
    QueueEmpty    bool
}

func (r PreflightResult) OK() bool {
    return r.DiskSpaceOK && r.QueueEmpty
}

func (r PreflightResult) Warnings() []string {
    var w []string
    if !r.BackupOK {
        w = append(w, fmt.Sprintf("no backup in the last %d days", backupWarnDays))
    }
    return w
}

func (o *Orchestrator) runPreflightChecks(ctx context.Context, flakeRef string) (*PreflightResult, error) {
    result := &PreflightResult{QueueEmpty: true} // TODO: check o.queue when IsProcessing() is available

    // Disk space check
    free, err := o.diskChecker()
    if err != nil {
        return nil, fmt.Errorf("disk check: %w", err)
    }
    result.DiskSpaceFree = free
    result.DiskSpaceOK = free >= minDiskBytes

    // Backup check — warn only (not blocking)
    result.BackupOK = true // TODO: query backup store when implemented

    return result, nil
}

// defaultDiskChecker returns free bytes on the data partition.
func defaultDiskChecker(dataDir string) func() (int64, error) {
    return func() (int64, error) {
        var stat syscall.Statfs_t
        if err := syscall.Statfs(dataDir, &stat); err != nil {
            return 0, err
        }
        return int64(stat.Bavail) * int64(stat.Bsize), nil
    }
}

// ApplyUpdate runs the full update flow.
// The host-agent self-restarts mid-update; the new binary calls ResumeUpdate
// on startup to continue from the last completed step.
func (o *Orchestrator) ApplyUpdate(ctx context.Context, req UpdateRequest) (*UpdateResult, error) {
    updateStateStore := store.NewUpdateStateStore(o.db)

    // 1. Preflight
    preflight, err := o.runPreflightChecks(ctx, req.FlakeRef)
    if err != nil {
        return nil, fmt.Errorf("preflight: %w", err)
    }
    if !preflight.OK() {
        return nil, fmt.Errorf("preflight failed: disk free %d bytes (need %d)", preflight.DiskSpaceFree, minDiskBytes)
    }

    // 2. Detect version from flake ref (best-effort; use date from flake ref or "unknown")
    version := versionFromFlakeRef(req.FlakeRef)

    // 3. Write durable update state before any mutations
    if err := updateStateStore.Save(store.UpdateState{
        Status: "in_progress", Step: "preflight",
        Version: version, FlakeRef: req.FlakeRef,
    }); err != nil {
        o.logger.Warn("failed to save update state", "error", err)
    }

    // 4. Advance to rebuild (dry-run skipped for initial implementation)
    if err := updateStateStore.Advance("rebuild"); err != nil {
        o.logger.Warn("advance step rebuild", "error", err)
    }

    // 5. nixos-rebuild switch against remote flake
    remoteRebuilder := nixgen.NewUpdateRebuilder(req.FlakeRef, o.logger)
    result, err := remoteRebuilder.Switch(ctx)
    if err != nil || (result != nil && !result.Success) {
        msg := "nixos-rebuild switch failed"
        if result != nil {
            msg = result.ErrorMessage
        }
        _ = updateStateStore.Fail(msg)
        // Auto-rollback
        if _, rbErr := o.Rollback(ctx); rbErr != nil {
            o.logger.Error("rollback failed after update failure", "error", rbErr)
        }
        return nil, fmt.Errorf("rebuild failed: %s", msg)
    }

    // 6. Self-restart: write 'restart' step, then restart service.
    // The new binary picks up at 'restart' via ResumeUpdate().
    if err := updateStateStore.Advance("restart"); err != nil {
        o.logger.Warn("advance step restart", "error", err)
    }
    o.selfRestart()

    // After self-restart, this goroutine is dead.
    // ResumeUpdate in the new process handles the rest.
    return nil, nil
}

// selfRestart triggers a systemctl restart of the host-agent service.
// The current process will be killed by systemd; do not rely on code after this point.
func (o *Orchestrator) selfRestart() {
    o.logger.Info("triggering self-restart for update")
    if err := o.rebuilder.StopUserService(context.Background(), "bloud-host-agent"); err != nil {
        o.logger.Error("self-restart failed", "error", err)
    }
}

// ResumeUpdate checks for an in-progress update on startup and continues from last step.
func (o *Orchestrator) ResumeUpdate(ctx context.Context) {
    updateStateStore := store.NewUpdateStateStore(o.db)
    state, err := updateStateStore.Load()
    if err != nil || state == nil || state.Status != "in_progress" {
        return
    }

    o.logger.Info("resuming update", "step", state.Step, "version", state.Version)

    switch state.Step {
    case "restart":
        if err := o.rebuilder.ReloadAndRestartApps(ctx); err != nil {
            o.logger.Error("restart apps failed during resume", "error", err)
            _ = updateStateStore.Fail(err.Error())
            return
        }
        _ = updateStateStore.Advance("reconcile")
        fallthrough

    case "reconcile":
        appResults, err := o.reconciler.ReconcileWithResults(ctx)
        if err != nil {
            o.logger.Error("reconcile failed during resume", "error", err)
            _ = updateStateStore.Fail(err.Error())
            return
        }

        hasWarnings := false
        for _, r := range appResults {
            if r.Status != "ok" {
                hasWarnings = true
                break
            }
        }

        if hasWarnings {
            o.logger.Warn("update completed with reconciliation warnings")
        } else {
            o.logger.Info("update completed successfully", "version", state.Version)
        }
        _ = updateStateStore.Complete()
    }
}

func versionFromFlakeRef(ref string) string {
    // Extract version hint from flake ref if it contains a date tag.
    // "github:owner/bloud/2026.03.15" → "2026.03.15"
    // "github:owner/bloud/stable" → "stable"
    parts := strings.Split(ref, "/")
    if len(parts) > 0 {
        return parts[len(parts)-1]
    }
    return "unknown"
}
```

- [ ] **Step 4: Add `NewUpdateRebuilder` to `nixgen/rebuild.go`**

```go
// NewUpdateRebuilder creates a Rebuilder targeting a remote flake for system updates.
// It uses "installed" as the NixOS attribute (hostname-independent).
func NewUpdateRebuilder(flakeRef string, logger *slog.Logger) *Rebuilder {
    return &Rebuilder{
        flakePath:      flakeRef,
        nixosAttribute: "installed",
        logger:         logger,
        impure:         true,
        useSudo:        true,
    }
}
```

- [ ] **Step 5: Add `db`, `diskChecker`, and `reconciler` fields to `Orchestrator`**

In `orchestrator_nix.go`, add three new fields to the `Orchestrator` struct (after the existing `queue` field):

```go
type Orchestrator struct {
    // ... existing fields unchanged ...
    db          *sql.DB
    diskChecker func() (int64, error)
    reconciler  *Reconciler
}
```

Also add to `Config`:

```go
type Config struct {
    // ... existing fields unchanged ...
    DB         *sql.DB
    Reconciler *Reconciler
}
```

In `New()`, after `o.queue = NewOperationQueue(...)`, add:

```go
o.db          = cfg.DB
o.diskChecker = defaultDiskChecker(cfg.DataDir)
o.reconciler  = cfg.Reconciler
```

In `main.go` (or `server.go` — wherever `orchestrator.New()` is called), pass `DB: db` and `Reconciler: reconciler` in the `Config`. The `reconciler` is already constructed; just add it to the orchestrator config.

Add `"database/sql"` import to `orchestrator_nix.go` if not already present.

- [ ] **Step 7: Run preflight tests to confirm they pass**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/orchestrator/... -run "TestPreflight" -v
```

Expected: PASS.

- [ ] **Step 8: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 9: Commit**

```bash
git add services/host-agent/internal/orchestrator/update.go \
        services/host-agent/internal/orchestrator/update_test.go \
        services/host-agent/internal/nixgen/rebuild.go
git commit -m "feat: implement ApplyUpdate, preflight checks, and ResumeUpdate"
```

---

### Task 14: Call `ResumeUpdate` on startup

**Files:**
- Modify: `services/host-agent/cmd/host-agent/main.go`

- [ ] **Step 1: Add `ResumeUpdate` call after orchestrator is initialized**

In `runServer()`, after the orchestrator and reconciler are created and wired into the server, add:

```go
// Resume any in-progress update (e.g. after self-restart mid-update).
go o.ResumeUpdate(context.Background())
```

The `go` keeps startup non-blocking; ResumeUpdate is a no-op if no update is in progress.

- [ ] **Step 2: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 3: Commit**

```bash
git add services/host-agent/cmd/host-agent/main.go
git commit -m "feat: call ResumeUpdate on startup to continue interrupted updates"
```

---

## Chunk 7: Update API

### Task 15: Implement update handlers

**Files:**
- Create: `services/host-agent/internal/api/update.go`
- Create: `services/host-agent/internal/api/update_test.go`

- [ ] **Step 1: Write failing tests**

Create `api/update_test.go`:

> **Note:** The existing API test helper is `setupTestServer(t) (*Server, string)`, not `newTestServer`.
> Use that. Also, `handleUpdateStatus` calls `store.NewUpdateStateStore(s.db)`. The `s.db` field
> must be non-nil. Inject the test DB via `setupTestServer` (which uses `testdb.SetupTestDB`).
> Check `api_test.go` to see how `setupTestServer` works and how `s.db` is set.
> If `setupTestServer` doesn't set `s.db`, add DB injection to it as part of this task.

```go
package api_test

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestHandleUpdateCheckReturnsStatus(t *testing.T) {
    s, _ := setupTestServer(t)

    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/api/system/update/check", nil)
    s.router.ServeHTTP(rec, req)

    assert.Equal(t, http.StatusOK, rec.Code)
    var body map[string]interface{}
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    assert.Contains(t, body, "currentVersion")
    assert.Contains(t, body, "flakeRef")
}

func TestHandleUpdateApplyReturnsBadRequestOnEmptyFlakeRef(t *testing.T) {
    s, _ := setupTestServer(t)

    body := `{"flakeRef": ""}`
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/api/system/update/apply",
        strings.NewReader(body))
    s.router.ServeHTTP(rec, req)

    // Handler must return 400 when flakeRef is empty — caller should use the
    // flakeRef returned by GET /api/system/update/check rather than omitting it.
    assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateStatusReturnsIdleWhenNoUpdate(t *testing.T) {
    s, _ := setupTestServer(t)

    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/api/system/update/status", nil)
    s.router.ServeHTTP(rec, req)

    assert.Equal(t, http.StatusOK, rec.Code)
    var body map[string]interface{}
    require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
    assert.Equal(t, "idle", body["status"])
}
```

> **Before running these tests**, check that `setupTestServer` in `api_test.go` sets `s.db` to the
> test database. If it doesn't, add `db: testdb.SetupTestDB(t)` to the `Server{}` construction
> inside `setupTestServer`. This is needed so `handleUpdateStatus` doesn't panic on a nil DB.

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/api/... -run "TestHandleUpdate" -v
```

Expected: 404s / compile errors.

- [ ] **Step 3: Implement `api/update.go`**

```go
package api

import (
    "encoding/json"
    "net/http"

    "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/orchestrator"
    "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// DefaultFlakeRef is the stable update channel URL returned by /check.
const DefaultFlakeRef = "github:owner/bloud/stable"

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
    respondJSON(w, http.StatusOK, map[string]interface{}{
        "currentVersion": s.currentVersion(),
        "flakeRef":       DefaultFlakeRef,
    })
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
    var req struct {
        FlakeRef string `json:"flakeRef"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusBadRequest, "invalid request body")
        return
    }
    if req.FlakeRef == "" {
        respondError(w, http.StatusBadRequest, "flakeRef is required; use GET /api/system/update/check to get the current value")
        return
    }

    go func() {
        if _, err := s.orchestrator.ApplyUpdate(r.Context(), orchestrator.UpdateRequest{
            FlakeRef: req.FlakeRef,
        }); err != nil {
            s.logger.Error("update failed", "error", err)
        }
    }()

    respondJSON(w, http.StatusAccepted, map[string]string{
        "status":  "started",
        "message": "Update started. Poll /api/system/update/status for progress.",
    })
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
    updateStateStore := store.NewUpdateStateStore(s.db)
    state, err := updateStateStore.Load()
    if err != nil {
        respondError(w, http.StatusInternalServerError, "failed to load update state")
        return
    }
    if state == nil {
        respondJSON(w, http.StatusOK, map[string]string{"status": "idle"})
        return
    }
    respondJSON(w, http.StatusOK, map[string]interface{}{
        "status":  state.Status,
        "step":    state.Step,
        "version": state.Version,
        "error":   state.Error,
    })
}

func (s *Server) currentVersion() string {
    // TODO: embed version at build time via -ldflags
    return "unknown"
}
```

- [ ] **Step 4: Register routes in `routes.go`**

In `routes.go`, in the protected system routes block, add:

```go
r.Get("/system/update/check", s.handleUpdateCheck)
r.Post("/system/update/apply", s.handleUpdateApply)
r.Get("/system/update/status", s.handleUpdateStatus)
```

- [ ] **Step 5: Run tests to confirm they pass**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go test ./internal/api/... -run "TestHandleUpdate" -v
```

Expected: PASS.

- [ ] **Step 6: Run full test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

- [ ] **Step 7: Commit**

```bash
git add services/host-agent/internal/api/update.go \
        services/host-agent/internal/api/update_test.go \
        services/host-agent/internal/api/routes.go
git commit -m "feat: add update API endpoints (check, apply, status)"
```

---

## Chunk 8: host-agent.nix fetchurl Mode

### Task 16: Add fetchurl fallback to `host-agent.nix`

**Files:**
- Modify: `nixos/packages/host-agent.nix`

This is the Nix-side change that enables `nixos-rebuild switch --flake github:...#installed` to fetch the host-agent binary and frontend from the GitHub release. CI will populate the SHA values before tagging a release.

- [ ] **Step 1: Add fetchurl constants and package to `host-agent.nix`**

After the existing `hasInstalled` block, add:

```nix
# Release version and artifact SHAs — updated by CI before each release tag.
# See .github/workflows/build-iso.yml for the update sequence.
releaseTag = "PLACEHOLDER_VERSION";
hostAgentSha256 = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
frontendSha256 = "sha256-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=";

hasFetchurlConfig = releaseTag != "PLACEHOLDER_VERSION";

fetchedPackage = pkgs.runCommand "bloud-host-agent-${releaseTag}" {} ''
  mkdir -p $out/bin $out/share/bloud/web/build $out/share/bloud/apps $out/share/bloud/nixos

  cp ${pkgs.fetchurl {
    url = "https://codeberg.org/d-buckner/bloud/releases/download/${releaseTag}/host-agent";
    sha256 = hostAgentSha256;
  }} $out/bin/host-agent
  chmod +x $out/bin/host-agent

  tar -xzf ${pkgs.fetchurl {
    url = "https://codeberg.org/d-buckner/bloud/releases/download/${releaseTag}/frontend.tar.gz";
    sha256 = frontendSha256;
  }} -C $out/share/bloud/web/build

  # App metadata, modules, and NixOS config from source (always from the flake, not pre-built)
  for app in ${src}/apps/*/; do
    appName=$(basename "$app")
    if [ -f "$app/metadata.yaml" ]; then
      mkdir -p "$out/share/bloud/apps/$appName"
      cp "$app/metadata.yaml" "$out/share/bloud/apps/$appName/"
      [ -f "$app/icon.png" ] && cp "$app/icon.png" "$out/share/bloud/apps/$appName/"
      [ -f "$app/module.nix" ] && cp "$app/module.nix" "$out/share/bloud/apps/$appName/"
      for subdir in "$app"*/; do
        [ -d "$subdir" ] && cp -r "$subdir" "$out/share/bloud/apps/$appName/"
      done
    fi
  done

  cp -r ${src}/nixos/* $out/share/bloud/nixos/ 2>/dev/null || true
  cp ${src}/flake.nix $out/share/bloud/
  cp ${src}/flake.lock $out/share/bloud/
'';
```

Update the final selection logic:

```nix
in
if hasPrebuilt then realPackage
else if hasInstalled then builtins.storePath deployedPkgRoot
else if hasFetchurlConfig then fetchedPackage
else stubPackage
```

- [ ] **Step 2: Verify the flake still checks cleanly (without triggering fetchurl)**

Since `releaseTag = "PLACEHOLDER_VERSION"`, `hasFetchurlConfig` is false and `stubPackage` is selected for `nix flake check`. Confirm:

```bash
cd /Users/daniel/Projects/bloud && nix flake check 2>&1 | grep -v "^warning"
```

Expected: passes without errors (stub is used, not fetchurl).

- [ ] **Step 3: Commit**

```bash
git add nixos/packages/host-agent.nix
git commit -m "feat: add fetchurl fallback to host-agent.nix for remote flake updates"
```

---

## Chunk 9: CI Pipeline

### Task 17: Embed SHAs in release workflow

**Files:**
- Modify: `.github/workflows/build-iso.yml`

The release workflow must be updated to: build artifacts → compute SHAs → write them into `host-agent.nix` → commit → tag → upload artifacts. The existing workflow currently builds and tags without the SHA embedding step.

- [ ] **Step 1: Read current workflow**

```bash
cat /Users/daniel/Projects/bloud/.github/workflows/build-iso.yml
```

- [ ] **Step 2: Add SHA embedding steps after artifact build, before ISO build**

Locate the section after Go binary and frontend are built. Add these steps:

```yaml
- name: Compute artifact SHAs
  id: sha
  run: |
    HA_SHA=$(nix-hash --type sha256 --base32 build/host-agent | \
      xargs -I{} nix hash to-sri --type sha256 {})
    FRONTEND_SHA=$(tar -czf /tmp/frontend.tar.gz -C build/frontend . && \
      nix-hash --type sha256 --base32 /tmp/frontend.tar.gz | \
      xargs -I{} nix hash to-sri --type sha256 {})
    echo "host_agent_sha=$HA_SHA" >> $GITHUB_OUTPUT
    echo "frontend_sha=$FRONTEND_SHA" >> $GITHUB_OUTPUT
    echo "release_tag=${{ steps.version.outputs.tag }}" >> $GITHUB_OUTPUT

- name: Embed SHAs into host-agent.nix
  run: |
    TAG="${{ steps.sha.outputs.release_tag }}"
    HA_SHA="${{ steps.sha.outputs.host_agent_sha }}"
    FE_SHA="${{ steps.sha.outputs.frontend_sha }}"
    # Use [^"]* to match any current value (not just the placeholder).
    # This ensures re-runs after the first release still update correctly.
    sed -i "s|releaseTag = \"[^\"]*\"|releaseTag = \"$TAG\"|" \
      nixos/packages/host-agent.nix
    sed -i "s|hostAgentSha256 = \"[^\"]*\"|hostAgentSha256 = \"$HA_SHA\"|" \
      nixos/packages/host-agent.nix
    sed -i "s|frontendSha256 = \"[^\"]*\"|frontendSha256 = \"$FE_SHA\"|" \
      nixos/packages/host-agent.nix

- name: Commit SHA update
  run: |
    git config user.name "github-actions[bot]"
    git config user.email "github-actions[bot]@users.noreply.github.com"
    git add nixos/packages/host-agent.nix
    git commit -m "chore: set release artifacts for ${{ steps.sha.outputs.release_tag }}"
    git push origin HEAD:main
```

> Note: The ISO build step must come **after** this commit so it incorporates the updated `host-agent.nix`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/build-iso.yml
git commit -m "ci: embed artifact SHAs into host-agent.nix before ISO build"
```

---

### Task 18: Add weekly app version bump workflow

**Files:**
- Create: `.github/workflows/update-app-versions.yml`

- [ ] **Step 1: Create the workflow file**

```yaml
name: Update app versions

on:
  schedule:
    - cron: '0 9 * * 1'  # Every Monday at 09:00 UTC
  workflow_dispatch:       # Allow manual trigger

jobs:
  update-versions:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Run version update script
        run: |
          DOCKER_CMD=docker ./scripts/update-app-versions.sh

      - name: Check for changes
        id: changes
        run: |
          if git diff --quiet; then
            echo "changed=false" >> $GITHUB_OUTPUT
          else
            echo "changed=true" >> $GITHUB_OUTPUT
          fi

      - name: Open PR if versions changed
        if: steps.changes.outputs.changed == 'true'
        uses: peter-evans/create-pull-request@v7
        with:
          commit-message: "chore: bump app versions"
          title: "chore: bump app versions ${{ github.run_id }}"
          body: |
            Automated weekly container image version update.

            Review each version bump before merging — check changelogs for breaking changes,
            especially for Authentik (migration steps may be required).
          branch: "chore/bump-app-versions"
          base: main
          delete-branch: true
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/update-app-versions.yml
git commit -m "ci: add weekly app version bump workflow"
```

---

## Final Verification

- [ ] **Run complete test suite**

```bash
cd /Users/daniel/Projects/bloud/services/host-agent && \
  go run gotest.tools/gotestsum@latest --format testdox ./...
```

Expected: all PASS, zero failures.

- [ ] **Verify no `:latest` image tags remain**

```bash
grep -rn "image.*:latest" /Users/daniel/Projects/bloud/apps/
```

Expected: no output.

- [ ] **Verify update routes are registered**

```bash
grep -n "system/update" /Users/daniel/Projects/bloud/services/host-agent/internal/api/routes.go
```

Expected: 3 lines for check, apply, status.

- [ ] **Verify flake evaluates with installed attribute**

```bash
cd /Users/daniel/Projects/bloud && \
  nix eval .#nixosConfigurations.installed.config.networking.hostName 2>&1
```

Expected: outputs the configured hostname without error.
