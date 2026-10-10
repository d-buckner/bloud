> Status: draft

# Plan: `apps/arr-mcp`, one MCP provider over the request and arr stack

**Last updated:** 2026-10-09

## Why

An agent that can answer "what did someone request and what is stuck" needs Seerr,
and Bloud has no MCP provider for it. The shape is the one already in the tree: a
wrapper as an ordinary catalog app that provides the `mcp` contract
([features/mcp.md](../features/mcp.md)).

The candidate is [`bardesss/arr-mcp`](https://github.com/bardesss/arr-mcp), one
server covering Seerr, the Servarrs, Jellyfin and others. It was spiked live
against Seerr v3.4.1: mandatory bearer verified (`401` with no header, `401` with
a wrong one, `200` with the right one), `tools/list` returning 39 tools, and
`stack_health` reporting the Seerr version over a real wiring.

The design decision this document records is that it declares **several optional
integrations** rather than one, and that this is invariant 15 working as intended
rather than a compromise.

## The shape: optional integrations, not a menu

Invariant 15 states the rule: an app declares every provider that can satisfy a
contract under `compatible:`, Bloud wires every one of them that is installed,
and it never picks one. arr-mcp is that rule with three contracts instead of one:

```yaml
integrations:
  requestManager:
    required: true
    compatible:
      - app: seerr
        default: true
  pvr:
    required: false
    multi: true
    compatible:
      - app: radarr
      - app: sonarr
  mediaServer:
    required: false
    compatible:
      - app: jellyfin
        default: true
```

Every one of these already has a catalog app: `seerr`, `radarr`, `sonarr`,
`jellyfin`. Installing more apps grows the config the configurator writes, the
declared edges order each provider's convergence, and the staleness re-run
re-runs `PostStart` when a provider transitions. No framework change is needed
beyond the one new contract below.

`requestManager` is `required: true` so an install always arrives with something
to serve. That choice is deliberate and is discussed under "Two things this
deliberately does not do".

`downloadClient` is out of scope for this change. It is not a rejection: the
contract carries no credential today, and adding the line later is a metadata
change plus a configurator branch, not a redesign.

## The credential model

Two directions, and both already have a working precedent in `apps/jellyfin-mcp`.

### Inbound: the `mcp` bearer the provider validates

Bloud generates the token, writes it into arr-mcp's `auth.tokens` as a SHA-256
hash, and publishes the plaintext under `provides.mcp.secrets.httpToken`. This is
the rule in [features/mcp.md](../features/mcp.md): the published `httpToken` is
always a credential the provider itself validates. It is the reason the first
candidate was rejected, and arr-mcp passes it: the spike confirmed the listener
rejects both a missing and an incorrect bearer.

### Outbound: what each provider hands over

| Contract | Carries | arr-mcp needs | Gap |
|---|---|---|---|
| `requestManager` | nothing yet | Seerr API key | gap 1 |
| `pvr` | `apiKey` | API key | none |
| `mediaServer` | `adminPassword`, `adminUsername` | API key | gap 2 |

**Gap 2 is already solved, and this is the part worth writing down.** Jellyfin
does not hand out an API key through a contract; it hands over the bootstrap
admin password. The mint that turns that password into a durable, named API key
already exists in `apps/jellyfin/api.go`, the Jellyfin app's own client:

- `EnsureAPIKey` is the exported entry point: it authenticates with the
  bootstrap admin login, adopts the named key if it exists, and creates it only
  when it does not. Lookup before create, so a resync does not accumulate keys
  in Jellyfin's Security screen.
- Each consumer holds its own named key (`jellyfin-mcp`, `arr-mcp`) and persists
  it with `secrets.SetAppSecret(appName, "jellyfinApiKey", key)`, so revocation
  stays per-consumer and a steady-state resync is a read-only diff.

The two wrappers share that one client rather than each carrying a copy, and one
app publishing another app's outbound credential is not a shape the contract
system has or should grow.

### Gap 1: the `requestManager` contract

Invariant 15 spells out what a new capability costs, and it is four pieces, not
one:

1. A registry entry in `internal/catalog/contracts.go`:
   `{Name: "requestManager", Secrets: []string{"apiKey"}, Values: []ValueSpec{{Key: "defaultUser"}}}`.
2. A payload type in `pkg/configurator`, embedding `ProviderRef`, next to
   `PVRBinding` and `MediaServerBinding`.
3. One arm in `bindContract` (`internal/engine/orchestrator/integrations.go`).
4. One slice on `Bindings` (`pkg/configurator/interface.go:519-527`),
   `RequestManagers []RequestManagerBinding`. Never a field added to a shared
   binding struct.

On the provider side, `apps/seerr` already reads the key Seerr generates for
itself out of `settings.json` (`readAPIKey`, `configurator.go:673`) and already
creates its admin account (`adminEmail = "bloud-admin@localhost"`,
`configurator.go:44`). What is missing is only that it never publishes either:
there is no `provides:` block in `apps/seerr/metadata.yaml` and no
`SetAppContractValue` call. Both are additions to code paths that exist.

`defaultUser` is a provider **value**, not a secret, and it is static metadata
rather than runtime-published, because Bloud creates that account itself. That is
exactly how `apps/jellyfin` offers `adminUsername: bloud-bootstrap-admin`. It is
not listed under `operatorValues`: the account is Bloud's own and there is no
operator override to offer. It is needed at all because arr-mcp refuses every
Seerr tool until one is named.

## The config UI credential is a client credential, not a new mechanism

arr-mcp's config UI is the only place a human can create or revoke a token, so it
cannot be left unreachable, and an unclaimed instance is a race where the first
device on the LAN wins and ends up holding every configured credential.

The platform already has the answer: **a client credential**. Bloud mints the UI
password, writes its hash into `config.yaml`, and reveals the plaintext to the
operator through the existing reveal flow
([features/client-credentials.md](../features/client-credentials.md)), declared
the way `apps/hermes-webui` declares its mobile password:

```yaml
provides:
  clientPassword:
    secrets:
      - password
    clientAccess:
      secret: password
      reveal: once
      rotate: bloud
      label: "Config UI password"
      reaches: "this app's configuration UI, which can add services and mint MCP tokens"
      snippet: url-and-password
```

`reaches` is required whenever `reveal` is not `never`, and the loader refuses a
revealable credential with no honest disclosure, which is the guard that makes
this safe rather than a password printed on a screen. `rotate: bloud` gives the
operator a rotation control instead of a recovery procedure.

This replaces an earlier idea in this plan of a create-once hash that Bloud
promised never to re-assert if an operator deleted it. That rule was a new
concept invented to solve a problem the `clientPassword` contract already solves,
and it is dropped.

## Conformance

These are the gates the repository already enforces, not new ones. `apps/arr-mcp`
owes all of them.

| Gate | Where | What it forces here |
|---|---|---|
| Conformance table complete | `apps/conformance_test.go:78` | A row in `conformanceTable`, or the build fails |
| PreStart offline | `configtest.AssertPreStartOffline` | With no bindings supplied, `PreStart` must make zero network calls; `OfflineDeps` blocks the network and names the address it tried |
| PreStart idempotent | `configtest.AssertPreStartIdempotent` | A second `PreStart` must not ask for a recreate. This is the oscillation test, already a gate |
| Port matches metadata | `configtest.AssertPortMatchesMetadata` | Constructor `defaultPort` equals `metadata.yaml` `port` |
| Teardown honest | `configtest.AssertTeardownHonest` | If it implements `configurator.Remover` it must be declared a teardown owner |
| Nil deps safe | `configtest.AssertNilDepsSafe` | `PreStart` survives a deps-less construction |
| Wait budget | `apps/configtest/waitbudget_test.go` | Any declared `appclient` `Wait.Within` must fit inside `AppPhaseBudget` |

The offline gate has a specific consequence here. `apps/jellyfin-mcp`'s row is
exercised in its **unbound** state, and its comment records the split: the bound
path that mints the Jellyfin key is covered by the app's own tests against a fake
server. arr-mcp has three optional bindings instead of one, so the same split
applies with more cases: the unbound config must be valid and written with no
network, and each bound path needs a fake-server test in
`apps/arr-mcp/configurator_test.go`.

It also has to be added to `validation.yaml` in the `apps:` block, with its auth
strategy, validation level, file globs and `e2e-project`, per
[guides/contributing-apps.md](../guides/contributing-apps.md).

## Two things this deliberately does not do

**It does not add a cross-contract "at least one of these N".** `required` is
per-contract, so Bloud cannot express "installing this needs some provider from
this set". The tempting fix is a new framework concept. The cheap one is what is
in the metadata above: make `requestManager` required, so the app always installs
with something to serve, and let the optional contracts only ever add. The
expressiveness gap is real and small; it is recorded rather than closed.

**It does not add a permission model.** arr-mcp supports per-instance permission
tiers and rejects an unknown value outright (the spike established the accepted
set is `read`, `write`, `destructive`). Because this app can hold credentials for
several services at once, Bloud sets the tiers explicitly on every pass rather
than inheriting upstream defaults, and defaults them to read. `apps/jellyfin-mcp`
does the equivalent with one blunt `--disable-destructive` flag; the per-instance
form is finer and should be used rather than matched. A test pins that a
steady-state pass writes the tiers it declares and nothing else.

## Config file ownership

Bloud owns `config.yaml` outright and rewrites it on every reconciliation with
`managedfile.Write`, the same shape `apps/jellyfin-mcp` uses for its env file:
not a marker merge. The image's config UI also saves over the same file, so a
service or token the operator adds there is overwritten on the next pass. That
is a stated limit of the first cut, not a design goal: Bloud already manages the
services (the contract bindings), the token and the UI password, so the UI is
only needed for things outside Bloud's catalog, which a later change can
preserve with a marker block if it ever matters.

`AssertPreStartIdempotent` is the gate that keeps this honest: the file is
written only when its bytes change, so a steady-state resync reports no change.

Health checking has a related trap the spike found: when its config fails
validation arr-mcp serves `503` on `/mcp` but `200` on `/healthz` with
`"status": "degraded"`. The conforming answer is the one `apps/jellyfin-mcp`
already uses: `/healthz` is a liveness probe and says only that the process is
up, and the functional gate moves to `PostStart`, where a failure can be reported
without flapping the container.

## Image pin

`ghcr.io/bardesss/arr-mcp:1.41.1@sha256:efa7b7addcbce5b761fd9e7aef00d33aedc89be0a095d51300af9514d9acd209`.
Multi-arch (amd64 and arm64), semver tags, so the tag carries intent and the
digest carries evidence, matching what `apps/jellyfin-mcp` does. Upstream ships
several releases a day, so the posture is: pin, bump deliberately, and treat a
bump as a review of the tool surface rather than a dependency refresh.

## Open questions

1. Tier enforcement against an onboarded Seerr. The spike could not reach the
   permission gate, because every write failed earlier on `default_user`. This
   needs a converged Seerr with a real user, which is the one thing a standalone
   spike cannot produce.

## Related

- [features/mcp.md](../features/mcp.md): the contract, the credential boundary,
  the rule this candidate passes and the first candidate failed.
- [features/client-credentials.md](../features/client-credentials.md): the reveal
  and rotation mechanism the config UI password uses.
- [plans/native-mcp-endpoint.md](native-mcp-endpoint.md): the tools no wrapper can
  write. Orthogonal to this one; installing apps grows that surface, installing
  this grows its own.
- [plans/media-stack-integration.md](media-stack-integration.md): the app-side
  wiring this provider reads rather than duplicates.
- [guides/contributing-apps.md](../guides/contributing-apps.md).
