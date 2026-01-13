# Bloud Testing Plan

This document outlines the comprehensive testing strategy for the orchestrator and NixOS modules.

## Overview

Two major testing gaps identified in the code review:
1. **Orchestrator Tests** - The core NixOrchestrator and Reconciler have zero test coverage
2. **NixOS Module Tests** - No nixosTest infrastructure exists for validating service startup

## Part 1: Go Orchestrator Tests

### Architecture

The orchestrator manages app installation/uninstallation through NixOS. Key files:

```
services/host-agent/internal/orchestrator/
├── interface.go           # AppOrchestrator interface, request/response types
├── orchestrator.go        # Podman-based orchestrator (fallback)
├── orchestrator_nix.go    # NixOS-based orchestrator (primary, ~969 lines)
└── reconcile.go           # Reconciliation loop (~279 lines)
```

### Interface Extraction

To enable mocking with testify/mock, interfaces are extracted for all dependencies:

| Package | Interface | Wraps | Status |
|---------|-----------|-------|--------|
| `store` | `AppStoreInterface` | `*store.AppStore` | ✅ Done |
| `catalog` | `CacheInterface` | `*catalog.Cache` | ✅ Done |
| `catalog` | `AppGraphInterface` | `*catalog.AppGraph` | ✅ Done |
| `nixgen` | `GeneratorInterface` | `*nixgen.Generator` | Pending |
| `nixgen` | `RebuilderInterface` | `*nixgen.Rebuilder` | Pending |
| `traefikgen` | `GeneratorInterface` | `*traefikgen.Generator` | Pending |
| `sso` | `BlueprintGeneratorInterface` | `*sso.BlueprintGenerator` | Pending |
| `authentik` | `ClientInterface` | `*authentik.Client` | Pending |
| `configurator` | `RegistryInterface` | `*configurator.Registry` | Pending |

### Test File Structure

```
services/host-agent/internal/orchestrator/
├── mocks_test.go              # testify/mock implementations
├── fixtures_test.go           # Test data helpers
├── orchestrator_nix_test.go   # NixOrchestrator tests (~39 tests)
└── reconcile_test.go          # Reconciler tests (~19 tests)
```

### NixOrchestrator Test Cases

#### Install Happy Path (8 tests)
| Test | Description |
|------|-------------|
| `TestInstall_SimpleApp_NoIntegrations` | Install app with no dependencies |
| `TestInstall_WithRequiredIntegration` | Install app with required integration |
| `TestInstall_WithAutoConfig` | Install app with single compatible installed |
| `TestInstall_WithUserChoice` | Install app with multiple compatible options |
| `TestInstall_WithDefaultSelection` | Install app where defaults apply |
| `TestInstall_MultipleDependencies` | Install app with multiple integrations |
| `TestInstall_SSOBlueprintGenerated` | Install app with native-oidc SSO |
| `TestInstall_TraefikRoutesRegenerated` | Any install regenerates routes |

#### Install Error Handling (6 tests)
| Test | Description |
|------|-------------|
| `TestInstall_UnknownApp` | Install non-existent app |
| `TestInstall_Blockers` | Install blocked by missing required integration |
| `TestInstall_TransactionBuildFails` | LoadCurrent returns error |
| `TestInstall_RebuildFails` | nixos-rebuild fails |
| `TestInstall_IntentRecordingFails` | Database write fails |
| `TestInstall_SSOBlueprintFails_NonFatal` | Blueprint generation fails (warning, continues) |

#### Install State Transitions (3 tests)
| Test | Description |
|------|-------------|
| `TestInstall_StatusFlow` | Verify status lifecycle: installing → starting → running |
| `TestInstall_GraphUpdated` | Graph state updated after install |
| `TestInstall_ExistingAppReinstall` | Reinstall already-installed app (UPSERT behavior) |

#### Uninstall (10 tests)
| Test | Description |
|------|-------------|
| `TestUninstall_SimpleApp` | Uninstall app with no dependents |
| `TestUninstall_WithClearData` | Uninstall with clearData=true |
| `TestUninstall_WithoutClearData` | Uninstall with clearData=false |
| `TestUninstall_SSOCleanup` | Uninstall SSO-enabled app |
| `TestUninstall_OrphanedApp` | Uninstall app not in Nix config |
| `TestUninstall_WillUnconfigure` | Uninstall with optional dependents |
| `TestUninstall_Blocked` | Uninstall with required dependents |
| `TestUninstall_RebuildFails` | nixos-rebuild fails during uninstall |
| `TestUninstall_DatabaseCleanupFails` | Database delete fails |
| `TestUninstall_SSOCleanupFails_NonFatal` | Authentik API fails (warning, continues) |

#### Health Checks (5 tests)
| Test | Description |
|------|-------------|
| `TestHealthCheck_NoHealthConfig` | App with no health check path |
| `TestHealthCheck_SuccessFirstTry` | Health endpoint responds 200 |
| `TestHealthCheck_SuccessAfterRetries` | Responds 200 after failures |
| `TestHealthCheck_Timeout` | Health never responds |
| `TestHealthCheck_NonSuccessStatus` | Returns 5xx |

#### State Watchdog (7 tests)
| Test | Description |
|------|-------------|
| `TestWatchdog_StuckInstalling` | App in installing > timeout |
| `TestWatchdog_StuckStarting` | App in starting > timeout |
| `TestWatchdog_StuckUninstalling` | App in uninstalling > timeout |
| `TestWatchdog_RunningButServiceDead` | Service not active |
| `TestWatchdog_RunningHealthFailed` | Health check fails |
| `TestWatchdog_ErrorRecovery` | Service/health recovered |
| `TestWatchdog_StopChannel` | Stop signal sent |

### Reconciler Test Cases

#### computeLevels (5 tests)
| Test | Description |
|------|-------------|
| `TestComputeLevels_NoDependencies` | Apps with no integrations |
| `TestComputeLevels_LinearChain` | A→B→C dependency chain |
| `TestComputeLevels_DiamondDependency` | A depends on B and C, both depend on D |
| `TestComputeLevels_MixedDeps` | Some apps have deps, some don't |
| `TestComputeLevels_UninstalledDepsIgnored` | Deps reference non-installed apps |

#### Reconcile Full Cycle (8 tests)
| Test | Description |
|------|-------------|
| `TestReconcile_EmptyApps` | No apps installed |
| `TestReconcile_SingleApp_NoConfigurator` | App without configurator |
| `TestReconcile_SingleApp_WithConfigurator` | App with configurator |
| `TestReconcile_MultiLevel_CorrectOrder` | Apps with dependencies |
| `TestReconcile_UninstallingSkipped` | App with status=uninstalling |
| `TestReconcile_PreStartFails` | PreStart returns error |
| `TestReconcile_HealthCheckFails` | HealthCheck times out |
| `TestReconcile_PostStartFails` | PostStart returns error |

#### buildAppState (2 tests)
| Test | Description |
|------|-------------|
| `TestBuildAppState_BasicFields` | App with port and name |
| `TestBuildAppState_Integrations` | App with integration config |

#### Watchdog (4 tests)
| Test | Description |
|------|-------------|
| `TestWatchdog_InitialReconcile` | StartWatchdog called |
| `TestWatchdog_PeriodicReconcile` | Wait for interval |
| `TestWatchdog_Stop` | StopWatchdog called |
| `TestWatchdog_ContextCanceled` | Context canceled |

---

## Part 2: NixOS Module Tests

### Test Structure

```
tests/nixos/
├── core-services.nix        # Postgres, Redis, Traefik startup
└── service-dependencies.nix # Dependency ordering (stretch goal)
```

### Core Services Test

Tests that postgres, redis, and traefik start correctly:

```nix
pkgs.nixosTest {
  name = "bloud-core-services";

  nodes.machine = { ... }: {
    imports = [ ../../nixos/bloud.nix ];
    bloud.enable = true;
    bloud.user = "bloud";
    bloud.apps.postgres.enable = true;
    bloud.apps.redis.enable = true;
    bloud.apps.traefik.enable = true;
  };

  testScript = ''
    machine.wait_for_unit("multi-user.target")
    machine.wait_for_unit("user@1000.service")

    # Verify postgres
    machine.succeed("sudo -u bloud systemctl --user start podman-apps-postgres.service")
    machine.wait_until_succeeds("sudo -u bloud podman exec apps-postgres pg_isready -U apps")

    # Verify redis
    machine.succeed("sudo -u bloud systemctl --user start podman-apps-redis.service")
    machine.wait_until_succeeds("sudo -u bloud podman exec apps-redis redis-cli ping | grep PONG")

    # Verify traefik
    machine.succeed("sudo -u bloud systemctl --user start podman-traefik.service")
    machine.wait_until_succeeds("curl -sf http://localhost:8080/api/overview")
  '';
}
```

### Flake Integration

Add to `flake.nix`:

```nix
checks = forAllSystems (system:
  let
    pkgs = import nixpkgs { inherit system; };
  in {
    core-services = import ./tests/nixos/core-services.nix { inherit pkgs; };
  }
);
```

---

## Implementation Phases

### Phase 1: Interface Extraction
- [x] Create `store/interfaces.go`
- [x] Create `catalog/interfaces.go`
- [ ] Create `nixgen/interfaces.go`
- [ ] Create `traefikgen/interfaces.go`
- [ ] Create `sso/interfaces.go`
- [ ] Create `authentik/interfaces.go`
- [ ] Create `configurator/interfaces.go`
- [ ] Add GetApps() method to AppGraph

### Phase 2: Update Orchestrator to Use Interfaces
- [ ] Update NixOrchestrator struct fields
- [ ] Update NixConfig struct
- [ ] Update Reconciler struct fields
- [ ] Update ReconcileConfig struct

### Phase 3: Create Mocks and Fixtures
- [ ] Create `mocks_test.go` with testify/mock implementations
- [ ] Create `fixtures_test.go` with test data helpers

### Phase 4: Implement Tests
- [ ] NixOrchestrator Install tests (17 tests)
- [ ] NixOrchestrator Uninstall tests (10 tests)
- [ ] Health check tests (5 tests)
- [ ] Watchdog tests (7 tests)
- [ ] Reconciler tests (19 tests)

### Phase 5: NixOS Tests
- [ ] Create `tests/nixos/` directory
- [ ] Create `core-services.nix`
- [ ] Update `flake.nix` with checks output

---

## Running Tests

### Go Tests

```bash
# Run all orchestrator tests
go test -v -cover ./services/host-agent/internal/orchestrator/...

# Run with race detection
go test -race ./services/host-agent/internal/orchestrator/...

# Run specific test
go test -v -run TestInstall_SimpleApp ./services/host-agent/internal/orchestrator/
```

### NixOS Tests

```bash
# Run all checks
nix flake check

# Run specific test
nix build .#checks.aarch64-linux.core-services

# Interactive debugging
nix build .#checks.aarch64-linux.core-services.driverInteractive
./result/bin/nixos-test-driver
```

---

## Test Patterns

Following existing codebase conventions:

1. **Table-driven tests** with testify/require and testify/assert
2. **testify/mock** for formal mock implementations
3. **t.TempDir()** for file operations
4. **httptest** for HTTP mocking
5. **SQLite** for database tests with test schemas
6. **Golden files** for generated output verification

---

## Estimated Scope

| Component | Files | Tests |
|-----------|-------|-------|
| Interface extraction | 7 files | - |
| Mocks + Fixtures | 2 files | - |
| NixOrchestrator tests | 1 file | ~39 tests |
| Reconciler tests | 1 file | ~19 tests |
| NixOS tests | 2 files | 2 test suites |
| **Total** | **13 files** | **~58 Go tests + 2 NixOS tests** |
