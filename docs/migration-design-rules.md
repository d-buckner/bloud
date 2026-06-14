# Portable Runtime Migration Design Rules

**Status:** Proposed  
**Purpose:** Use the portable-runtime migration to reduce technical debt without creating a
second implementation or an unbounded rewrite.

## Core Rule

Every migration task should leave behind:

1. A runtime-neutral contract.
2. A characterization or contract test.
3. Less NixOS-specific shared code.
4. No new application-specific branch in shared orchestration.
5. A clear deletion path for the old implementation.

The migration is not permission for unrelated cleanup. Improvements must directly support
portable execution, integration reconciliation, or release reliability.

## Priority Improvements

### 1. Decompose the Existing Orchestrator

The current NixOS orchestrator mixes planning, persistence, Nix generation, rebuilds,
routing, SSO, health, status transitions, and cleanup.

The migration must extract runtime-neutral responsibilities from the current orchestrator:

```text
planner       calculates desired changes
executor      applies runtime operations
reconciler    converges desired and observed state
operation log records durable progress and failure
```

Do not build a separate portable orchestrator beside the existing one. Extract shared
behavior, temporarily connect both runtime implementations, then delete the NixOS adapter.

### 2. Introduce Explicit Domain Types

Current concepts are fragmented across catalog models, installed-app records, Nix
transactions, configurator state, and string statuses.

Introduce runtime-neutral domain types incrementally:

```go
type AppID string
type IntegrationType string
type Revision string
type Phase string

type DesiredApp struct { ... }
type ObservedApp struct { ... }
type IntegrationInstance struct { ... }
type ProviderOutput struct { ... }
type Operation struct { ... }
```

Translate to API, database, and runtime-specific representations at boundaries.

### 3. Evolve Configurators Through Compatibility Adapters

The current configurator interface uses `PreStart`, `HealthCheck`, and `PostStart`, and
receives integrations as `map[string][]string`.

The target contract uses typed resolved integrations:

```go
StaticConfig(context.Context, AppState) (ConfigResult, error)
HealthCheck(context.Context, AppState) error
DynamicConfig(context.Context, AppState) error
```

Do not port every configurator at once. Introduce compatibility adapters so existing
configurators continue working while apps move one at a time.

### 4. Make Planning Pure and Serializable

Planning must calculate complete desired changes without applying them:

- Recursive dependency installation
- Desired topology
- Resolved integration instances
- Invalidations
- Selective restarts
- Removal effects and blockers

Plans should be deterministic, serializable, inspectable in the dashboard and CLI, and
directly usable in tests.

### 5. Separate Durable Operations From Observed App Status

Application status describes observed state. It must not double as an operation journal.

Durable operations record phases and outcomes:

```text
ensure-topology
static-config
start
health
dynamic-config
complete
```

This enables crash recovery, safe retries, accurate progress, and actionable errors.

### 6. Make Portable Manifests Authoritative

During inventory, classify every current behavior as one of:

- Portable manifest topology
- Provider contract
- Configurator behavior
- Runtime adapter behavior
- Unsupported special case

Reject hidden behavior and contradictions. Shared orchestration must not contain
application-specific behavior that belongs in one of these contracts.

### 7. Separate App Configuration From Integration Configuration

Some desired state belongs to the application itself. Other desired state belongs to a
specific provider-consumer edge.

The domain model and revision tracking must allow these to be reconciled independently:

```go
ConfigureApp(appState)
ConfigureIntegration(integrationState)
```

Existing configurators may remain app-level during migration, but new state models must not
prevent edge-level reconciliation later.

### 8. Use Typed Errors

Replace formatted error strings and ambiguous booleans at domain boundaries:

```go
type PhaseError struct {
    App         AppID
    Integration *IntegrationID
    Phase       Phase
    Retryable   bool
    Cause       error
}
```

Typed failures drive retry policy, dashboard messages, logs, and tests.

### 9. Centralize Managed File Writes

Static configurators require trustworthy change detection. Use a shared atomic managed-file
API that:

- Compares desired and existing contents
- Writes atomically
- Applies explicit permissions
- Avoids rewriting unchanged files
- Redacts secret contents from logs and errors

Configurators must not each reimplement this behavior.

### 10. Prefer Contract-Oriented Fakes

Tests should verify domain decisions and adapter calls, not private implementation details.

Prefer small recording fakes:

```go
plan := planner.Plan(desired, observed)
result := reconciler.Apply(plan, fakeRuntime)
assert.Equal(t, expectedCalls, fakeRuntime.Calls())
```

Avoid expanding large behavior-heavy mock suites when a narrow contract fake is sufficient.

## Deletion Targets

Delete these only after portable parity is proven:

- Nix generator and rebuilder
- ISO installer
- NixOS app modules and helpers
- Rootless Podman networking workarounds
- NixOS-native PostgreSQL and Redis assumptions
- Proxmox/ISO-only release validation
- NixOS-specific CLI commands

Keeping deprecated runtime paths indefinitely would recreate the scope problem.

## Explicit Non-Goals

Do not use the migration to:

- Rewrite the dashboard
- Replace the API framework
- Replace the database without demonstrated need
- Rename or reorganize unrelated packages
- Support multiple distributions or container runtimes
- Build a generic plugin ecosystem
- Refactor code without characterization tests

## Review Checklist

Before accepting a migration change:

- Does it establish or use a runtime-neutral contract?
- Is existing behavior characterized before it changes?
- Can its core behavior be tested without Podman, systemd, or NixOS?
- Does it avoid new application-specific orchestration branches?
- Does it preserve a compatibility path for current behavior?
- Does it identify what old code becomes deletable later?
- Are failure and retry semantics explicit?
