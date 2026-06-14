# Slice 001: Typed Integration Resolution

**Status:** Implemented
**Timebox:** A few hours
**Risk:** Low
**Runtime behavior change:** Invalid desired integration state now fails explicitly

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

The first slice introduces typed integration bindings as a pure boundary. It also removes
the generic integration map from configurator state rather than carrying a legacy contract
into the target architecture.

## Goal

Create a deterministic resolver that turns current catalog and installed-app state into
typed resolved integration bindings:

```go
type AppID string
type IntegrationType string

type Bindings map[IntegrationType]AppID
```

The consumer is implicit in the resolution request. Requiredness and resolution provenance
are inputs or process details, not properties needed by downstream callers today. Durable
integration instances remain a later model with separately justified fields.

```go
type ResolutionInput struct {
    Requirements   map[IntegrationType]Requirement
    BoundProviders map[IntegrationType]AppID
    Installed      map[AppID]struct{}
}
```

The resolver does not know about configurators or legacy representations. Current
configurators only need to know whether to configure SSO, so their state receives an
explicit `SSOEnabled bool` instead of resolved provider bindings they do not consume.

## Scope

### Included

- New runtime-neutral integration domain package
- Pure deterministic resolution of currently declared integrations
- Persisted provider bindings from `InstalledApp.IntegrationConfig`
- Optional integrations when a compatible provider is installed
- Deterministic resolution
- Use of the resolver in `Reconciler.buildAppState`
- Removal of the generic configurator integration map
- Removal of unused configurator state fields
- Explicit `SSOEnabled` configurator capability
- Invalid binding errors propagated through reconciliation
- Focused unit tests
- Existing reconciler/orchestrator test suite

### Excluded

- Provider outputs
- Integration revisions or database persistence
- Portable manifests
- Debian runtime adapters
- Recursive install planning changes
- SSO model redesign

## Important Separation Decision

An SSO strategy is not a provider application. The resolver handles provider bindings;
configurator state separately exposes whether the app should configure its declared SSO
strategy. A later slice may add a typed SSO contract when configurators need strategy or
provider details.

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
type ResolutionInput struct {
    Requirements   map[IntegrationType]Requirement
    BoundProviders map[IntegrationType]AppID
    Installed      map[AppID]struct{}
}
```

Catalog/store translation happens at the reconciler boundary.

Every exported type and field must have a current production consumer or affect current
resolution policy. The boundary intentionally excludes consumer identity, resolution
provenance, copied requiredness, revisions, and status until a concrete caller requires
them.

## Resolution Rules

For each declared integration:

1. If a provider binding already exists, resolve that provider. Bindings are created by
   installation planning; there is currently no UI for customizing provider resolution.
2. Otherwise, if the integration is optional, select the first installed compatible provider
   using catalog order, matching current behavior.
3. A compatible bound provider resolves even if it is not currently installed, matching
   the existing `IntegrationConfig` behavior. Provider health/readiness is outside this slice.
4. A required integration without a binding is invalid desired state and returns an error.
5. An optional integration with no installed compatible provider produces no binding.
6. Resolve at most one provider for each integration type.

The resolver rejects undeclared bindings and bound providers that are not compatible.
Optional discovery never overrides an existing binding.

## Implementation Steps

1. Add resolver tests that characterize current required and optional integration behavior.
2. Add typed domain types and pure resolver.
3. Replace the integration-building portion of `buildAppState` with the resolver.
4. Replace generic configurator integrations with the explicit SSO capability currently
   consumed by configurators.
5. Propagate invalid binding errors rather than preserving invalid state.
6. Run focused and full host-agent tests.

## Required Tests

Pure resolver tests:

- Empty requirements produce no bindings.
- Bound required provider resolves.
- Bound provider takes precedence over optional discovery.
- Optional installed provider resolves.
- Optional absent provider does not resolve.
- Multiple optional compatible providers preserve current first-match behavior.
- Required integration without a binding returns an error.
- Bound incompatible provider returns an error.
- Bound undeclared integration returns an error.
- All declared bindings resolve deterministically.

Reconciler characterization tests:

- Required and optional bindings resolve independently of configurator state.
- Non-SSO bindings do not leak into configurator state.
- Jellyfin receives `SSOEnabled` from its declared LDAP strategy.
- Immich receives `SSOEnabled` from its declared OIDC strategy.
- An SSO provider binding without a declared strategy does not enable SSO configuration.
- Incompatible bindings fail instead of falling back.

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

No VM, NixOS rebuild, Podman, or browser test is required because the slice changes only
pure resolution and configurator input boundaries.

## Exit Criteria

- Typed integration bindings exist in a runtime-neutral package.
- Resolution is deterministic and fully unit tested.
- `Reconciler.buildAppState` uses the resolver.
- Configurator state contains only currently consumed inputs.
- Existing host-agent tests pass.
- No database schema or runtime-specific code is introduced.
- Future provider contracts and durable integration instances are added only when their
  consumers and required fields are concrete.

## Follow-Up Slices

Likely next slices, reviewed independently:

1. Separate SSO provider identity from SSO strategy.
2. Add typed provider outputs and secret references.
3. Add a managed-file helper and migrate one static configurator.
4. Add desired/observed integration revisions.
5. Adapt one configurator from `PreStart`/`PostStart` to static/dynamic contracts.
