# Slice 001: Typed Integration Instances

**Status:** Implemented
**Timebox:** A few hours
**Risk:** Low
**Runtime behavior change:** None intended

## Why This Slice

The portable architecture depends on integration edges being first-class domain objects.
Currently, resolved integrations are represented in several incompatible forms:

- Catalog declarations in `catalog.Integration`
- Planner-created provider bindings in `map[string]string`
- Configuration tasks in `catalog.ConfigTask`
- Configurator inputs in `AppState.Integrations map[string][]string`
- SSO strategy injected as a synthetic `"sso"` integration value

This makes it difficult to add provider outputs, revisions, durable status, or edge-level
reconciliation safely.

The first slice introduces typed integration instances as a pure boundary while adapting
them back into the existing configurator input. It creates a foundation without requiring
Podman, Debian, database schema, or configurator changes.

## Goal

Create a deterministic resolver that turns current catalog and installed-app state into
typed resolved integration instances:

```go
type AppID string
type IntegrationType string

type IntegrationInstance struct {
    Consumer AppID
    Provider AppID
    Type     IntegrationType
    Required bool
    Source   ResolutionSource
}
```

Initial resolution sources:

```go
const (
    ResolutionBound ResolutionSource = "bound"
    ResolutionOptional ResolutionSource = "optional-installed"
)
```

Then provide a compatibility adapter:

```go
func LegacyIntegrationMap(instances []IntegrationInstance) map[string][]string
```

The current reconciler uses this adapter to populate `configurator.AppState.Integrations`.
Existing configurators continue receiving the same values.

## Scope

### Included

- New runtime-neutral integration domain package
- Pure deterministic resolution of currently declared integrations
- Persisted provider bindings from `InstalledApp.IntegrationConfig`
- Optional integrations when a compatible provider is installed
- Stable sorting and duplicate elimination
- Compatibility conversion to the current string map
- Use of the resolver in `Reconciler.buildAppState`
- Focused unit tests
- Existing reconciler/orchestrator test suite

### Excluded

- Provider outputs
- Integration revisions or database persistence
- Static/dynamic configurator interface changes
- Portable manifests
- Debian runtime adapters
- Recursive install planning changes
- SSO model redesign
- Changes to current provider-resolution behavior

## Important Compatibility Decision

The current reconciler injects the SSO strategy into configurator state:

```text
state.Integrations["sso"] = ["native-oidc"]
```

That value is a strategy, not a provider application, and therefore is not a valid typed
integration instance.

For this slice:

- Typed resolution handles declared provider-consumer integrations only.
- The existing synthetic SSO strategy injection remains as a clearly marked compatibility
  fallback when no declared provider resolves.
- A later slice will model SSO provider and strategy separately.

This preserves current behavior without modeling an override mechanism:

- Jellyfin has no declared SSO integration and receives `sso: ["ldap"]`.
- Immich declares `sso -> authentik`, so it receives the installed provider:
  `sso: ["authentik"]`.

The overload is technical debt, but changing it belongs in a separately tested slice.

## Proposed Package Boundary

```text
services/host-agent/internal/integration/
  types.go
  resolver.go
  resolver_test.go
```

The package must not import orchestrator, store, database, Podman, Nix, or configurator
packages. Prefer small resolver-owned input types rather than coupling the domain package to
persistence models.

Example input:

```go
type AppDefinition struct {
    ID           AppID
    Integrations map[IntegrationType]Requirement
}

type ResolutionInput struct {
    Consumer       AppID
    Requirements   map[IntegrationType]Requirement
    BoundProviders map[IntegrationType]AppID
    Installed      map[AppID]bool
}
```

Catalog/store translation happens at the reconciler boundary.

## Resolution Rules

For each declared integration:

1. If a provider binding already exists, resolve that provider. Bindings are created by
   installation planning; there is currently no UI for customizing provider resolution.
2. Otherwise, if the integration is optional, select the first installed compatible provider
   using catalog order, matching current behavior.
3. A compatible bound provider resolves even if it is not currently installed, matching
   the existing `IntegrationConfig` behavior. Provider health/readiness is outside this slice.
4. Otherwise, produce no instance. Required-provider installation and blockers remain the
   install planner's responsibility in this slice.
5. Never produce duplicate consumer/provider/type instances.
6. Return instances in stable order by integration type, then provider.

The resolver rejects a bound provider that is not compatible. Optional discovery never
overrides an existing binding.

## Implementation Steps

1. Add resolver tests that characterize current required and optional integration behavior.
2. Add typed domain types and pure resolver.
3. Add the legacy-map compatibility adapter.
4. Add a small translation function at the reconciler boundary.
5. Replace the integration-building portion of `buildAppState` with the resolver.
6. Preserve the synthetic SSO strategy fallback as a compatibility step.
7. Run focused and full host-agent tests.

## Required Tests

Pure resolver tests:

- Empty requirements produce no instances.
- Bound required provider resolves.
- Bound provider takes precedence over optional discovery.
- Optional installed provider resolves.
- Optional absent provider does not resolve.
- Multiple optional compatible providers preserve current first-match behavior.
- Bound incompatible provider returns an error.
- Duplicate inputs do not produce duplicate instances.
- Output ordering is deterministic.
- Legacy-map conversion is deterministic.

Reconciler characterization tests:

- Existing required integration appears unchanged in `AppState`.
- Existing optional installed integration appears unchanged.
- Existing optional absent integration remains absent.
- Jellyfin's synthetic SSO strategy remains unchanged.
- Immich continues to receive its installed declared SSO provider.

## Validation Commands

Use a writable temporary Go cache so validation is independent of host cache permissions:

```bash
GOCACHE=/tmp/bloud-go-cache go test ./internal/integration ./internal/orchestrator
GOCACHE=/tmp/bloud-go-cache go test ./...
```

Then run the repository validation tier once its Go cache handling is made hermetic:

```bash
./bloud validate --tier fast
```

No VM, NixOS rebuild, Podman, or browser test is required because the slice intentionally
preserves runtime behavior and changes only a pure domain boundary plus compatibility adapter.

## Exit Criteria

- Typed integration instances exist in a runtime-neutral package.
- Resolution is deterministic and fully unit tested.
- `Reconciler.buildAppState` uses the resolver.
- Existing configurator inputs are unchanged.
- Existing host-agent tests pass.
- No database schema or runtime-specific code is introduced.
- The next slices can add provider outputs and revisions without changing the resolver's core
  identity model.

## Follow-Up Slices

Likely next slices, reviewed independently:

1. Separate SSO provider identity from SSO strategy.
2. Add typed provider outputs and secret references.
3. Add a managed-file helper and migrate one static configurator.
4. Add desired/observed integration revisions.
5. Adapt one configurator from `PreStart`/`PostStart` to static/dynamic contracts.
