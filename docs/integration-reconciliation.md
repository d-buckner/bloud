# Integration and Reconciliation Architecture

**Status:** Target architecture  
**Scope:** Application dependencies, configurators, invalidation, and recovery

## Core Principle

An integration edge is active desired state.

Bloud does not only start applications in dependency order. It continuously ensures that
each resolved provider/consumer relationship is correctly configured.

```text
consumer -> provider -> integration type

immich   -> postgres  -> database
immich   -> redis     -> cache
immich   -> authentik -> sso
jellyfin -> authentik -> ldap
```

SSO is one integration type. The same engine configures databases, caches, API keys,
application-to-application links, routes, and other dependencies.

## Three Separate Models

### Desired Topology

Which processes and runtime resources should exist:

- Services and containers
- Networks and volumes
- Ports and routes
- Health checks
- Startup dependencies

### Integration Graph

Which applications provide and consume typed capabilities:

```yaml
provides:
  database:
    capabilities: [postgresql, pgvector]

consumes:
  database:
    required: true
    providers: [postgres]
    capabilities: [pgvector]
```

### Integration Instances

Each resolved edge is persisted and observed:

```go
type IntegrationInstance struct {
    Consumer         string
    Provider         string
    Type             string
    DesiredRevision  string
    ObservedRevision string
    Status           string
    LastError        string
}
```

Revisions change when relevant provider outputs, consumer inputs, secrets, or configurator
versions change.

## Typed Provider Outputs

Providers expose structured contracts. Consumers do not discover provider details or depend
on undocumented runtime implementation.

Example database output:

```go
type DatabaseProvider struct {
    Endpoint        string
    Database        string
    Username        string
    PasswordSecret  string
    Capabilities    []string
}
```

The secret reference identifies a value in the secrets manager; it does not place plaintext
credentials in the integration graph.

## Configurator Contract

Configurators realize app and integration desired state. They receive structured application
state and resolved provider outputs.

They must be:

- Idempotent
- Deterministic for the same inputs
- Explicit about changed static output
- Safe to retry after partial failure
- Independently testable

### Static Configuration

Static configuration manages inputs consumed at process startup:

- Environment files
- Config files
- Certificates
- Provider endpoints and credentials
- Startup-time integration settings
- Required directories

```go
StaticConfig(ctx, state) (changed bool, err error)
```

`changed` means managed startup input differs from the prior applied state. The reconciler
uses it to restart only affected services.

Static configuration may provision provider-side resources required before consumer start,
such as a database and user, when that operation is idempotent and part of the typed provider
contract. Required providers must already be healthy before this phase runs.

### Health

Health verifies that the provider or consumer is ready for the next reconciliation phase.
Process existence alone is not health.

### Dynamic Configuration

Dynamic configuration manages runtime state after required services are healthy:

- API-based settings
- API keys
- Inter-app registration
- Runtime resource creation
- Authentik providers, applications, outposts, and LDAP setup

```go
DynamicConfig(ctx, state) error
```

Dynamic configuration must be idempotent and does not itself trigger a restart.

## Reconciliation Flow

```text
1. Load manifests and durable desired state
2. Resolve providers and calculate integration instances
3. Calculate desired topology and dependency levels
4. For each dependency level:
   a. Ensure topology for apps in the level exists
   b. Wait for each app's required providers to be healthy
   c. Run each app's static configuration with resolved provider outputs
   d. Start or selectively restart changed apps
   e. Verify app health
   f. Run dynamic configuration
   g. Record observed application and integration revisions
```

Independent apps within a dependency level may run concurrently. An app never runs static
or dynamic integration configuration before its required providers are healthy.

## Invalidation

Events invalidate integration instances and affected consumers:

- Provider installed or removed
- Provider output changed
- Secret changed
- Consumer manifest changed
- Configurator version changed
- Optional provider became healthy
- Prior configuration failed

Invalidations are durable and deduplicated. They remain pending until configuration succeeds
or the desired relationship is removed.

### Selective Restart

When provider outputs change:

```text
find affected integration instances
        |
run consumer StaticConfig
        |
restart consumers whose managed startup input changed
        |
verify health
        |
run DynamicConfig
```

Dynamic-only changes must not restart consumers.

## Failure and Recovery

An integration is `configured` only after all required phases succeed. Durable state records:

- Desired revision
- Last successfully observed revision
- Current phase
- Last error
- Retry eligibility
- Last successful time

After host-agent termination or reboot, reconciliation resumes from desired and observed
state. It must not rely on an in-memory operation having completed.

## Testing Contract

The integration engine requires tests for:

- Provider resolution and compatibility
- Required and optional integrations
- Dependency cycles
- Provider install/remove propagation
- Provider output and secret changes
- Static `changed` accuracy
- Dynamic idempotency
- Selective restarts
- Dependency ordering
- Partial failure and retry
- Durable invalidation recovery
- No partial-success reporting

Each app configurator also requires contract tests using structured provider outputs. A
successful end-to-end test does not replace configurator unit and contract tests.
