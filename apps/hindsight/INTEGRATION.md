# Hindsight Integration

## Status: Working (live-verified against the real image)

Every claim below was measured on `ghcr.io/vectorize-io/hindsight:1.0.0` on this
machine, not read out of upstream's documentation and trusted. Where the docs and
the image disagree, the image is what this app is built against, and the
disagreement is recorded.

| Fact | How it was checked |
| --- | --- |
| `/health` answers `{"status":"healthy","database":"connected"}` and needs no credential | `curl` against the running container |
| `/mcp` answers 401 with no bearer and completes the MCP handshake with one | `curl` POST, `initialize` |
| The handshake names itself `hindsight-mcp-server` | response body, captured in `api_test.go` |
| `HINDSIGHT_API_DATABASE_URL` is the database knob, and its default is an embedded database | container log line, then the same container pointed at a real PostgreSQL |
| `HINDSIGHT_CP_DATAPLANE_API_KEY` is required once the API is protected | the UI 401s on every panel without it, and returns 200 with it |
| `HINDSIGHT_CP_ACCESS_KEY` gates the UI | `POST /api/auth/login` returns 200 for the key and 401 for anything else |
| `HINDSIGHT_API_LLM_PROVIDER=none` is a supported boot, not a degraded one | the server reaches `healthy` with it and no endpoint configured |
| The gate's two routes split correctly | nginx in the same podman network, both paths plus `/mcpsomething` |
| The resolver address is only knowable inside the container | host `/etc/resolv.conf` named `10.0.0.1`, the container's named `10.89.0.1` |

## Why this app is three containers

Hindsight ships one image that runs two processes: a memory API on 8888 and a
Next.js Control Plane on 9999. Its `start-all.sh` starts the API, waits for
`/health`, starts the Control Plane, and takes the container down if either
process dies. There is nothing for the graph to order inside it, so splitting it
would be the graph modelling a supervision script.

That leaves two problems Bloud's app model has to solve around it.

The first is that an app has one port. Bloud publishes one port per app, routes
its subdomain to it, and an `extraPorts` entry is reached through a path prefix on
that same subdomain only when the port is separately reachable on the host, which
is exactly what this app must not do: the memory API must not be dialable from the
LAN. So a third container, nginx, holds the app's only published port and branches
it. `/mcp` goes to 8888 and everything else goes to 9999. This is the shape
`apps/ha-mcp` already uses for the same reason.

The second is that the app needs a database, and its default is a trap.

## The database, and the default that silently loses everything

`HINDSIGHT_API_DATABASE_URL` defaults to `pg0`, an embedded PostgreSQL that runs
inside the API container and writes to a directory nobody mounted. Measured: with
that variable unset, the container came up healthy, answered `/health` with
`"database":"connected"`, served the UI, and logged `Database: pg0`. The
`apps-hindsight-postgres` container sat on the network doing nothing, and every
byte written would have disappeared on the next recreate.

Nothing in the startup output calls this out, because from the server's point of
view an embedded database is a perfectly good choice. It is upstream's documented
quick start. It is simply the wrong one for a home server whose whole job is to
keep things.

So `HINDSIGHT_API_DATABASE_URL` is the one environment variable on that container
that is not a convenience. Setting it is what makes the postgres container the
database rather than decoration, and the graph edge to it is what makes the URL
resolvable at the moment the API runs its migrations.

pg16 rather than pg18 for the reason recorded in `metadata.yaml`: PostgreSQL 18
made `PGDATA` version-specific, so a bind mount written for one major version
reads as an empty directory to the next one.

## Two credentials, for two audiences

`mcp` carries `httpToken`. This is the machine half: the bearer the API's own
`ApiKeyTenantExtension` checks on every request, including the ones the Control
Plane makes on a user's behalf. It is a credential the provider validates, not a
string Bloud invented and hopes the app agrees with, which is what makes
`reveal: always` on it honest.

`clientPassword` carries `password`. This is the human half. Hindsight cannot join
Authentik: it has no OIDC client, no trusted-header mode, and no proxy-auth hook,
so `sso.strategy: none` is the honest declaration and the Control Plane's access
key is the sign-in. Bloud mints it, reveals it once, and rotates it by rewriting
the env file and asking for a recreate, which is what makes `rotate: bloud` a
mechanism rather than a label.

The two are not interchangeable, and the Control Plane needs both at once. It
authenticates the browser with the access key and then calls the API with the
bearer. Measured with only the access key set: the UI loads, signs in, and every
panel returns `{"error":"Authentication failed: Invalid API key"}`. That is the
failure `HINDSIGHT_CP_DATAPLANE_API_KEY` prevents, and it is why the env file
writes the bearer twice, once for the API and once for the Control Plane.

## The gate, and the resolver only the container can name

The gate proxies to `apps-hindsight` by name through an nginx `resolver`
directive, not through a literal `proxy_pass`. nginx resolves a literal upstream
name once, at config load, and then never again. A recreate of the server
container gives it a new address, and a gate holding the old one answers 502
forever with a perfectly healthy app behind it.

The resolver address is the awkward part. podman puts a user-defined network's DNS
at that network's gateway, and the gateway address depends on how many networks
already exist on the machine. Measured on one host: the host's own
`/etc/resolv.conf` named `10.0.0.1` and the container's named `10.89.0.1`. Only
the second one answers for `apps-hindsight`. A configurator that read the host's
resolv.conf and substituted it would produce a config that loads, starts, and
resolves nothing.

So the rendered file keeps the literal token `@RESOLVER@` and the container's own
command substitutes it from its own `resolv.conf` before starting nginx. The
result goes to `/tmp`, not back over the mount: the mount is read-only, and even
if it were writable, a container editing a file Bloud manages would make every
reconciliation pass see a diff, restart the gate, and never settle.

`nginxResolverScript` in `gate.go` and the `command:` in `metadata.yaml` are the
same script, and `TestGateCommandMatchesMetadata` fails if the two drift apart.

### The route is a regex, not a prefix

The MCP endpoint is both `/mcp` and `/mcp/<bank>/`. A plain nginx prefix location
would also claim `/mcpsomething`, which no upstream serves, so the match is
`^/mcp(/|$)`. `TestMCPRouteClaimsOnlyTheMCPPath` pins that against both families
of path, and `TestMCPRouteMatchesThePublishedContractValue` pins the route against
the `path: /mcp` the contract advertises, so a consumer is never handed a path the
gate does not serve.

### The locked shape

While either credential is missing the gate serves nothing but its own health
route: 401 on the MCP path, 503 everywhere else. Rendering the proxy with an empty
bearer would be the one configuration worse than a refusal, because the API would
then be reachable with no credential in front of it. Each half locks on its own
credential, so a rotation of one never opens the other surface.

## The generated env file

`{{appDataDir}}/config/env`, written on every pass and never mounted: the runtime
reads it on the host at create time, so the credentials stay out of the container
spec and out of `podman inspect`.

```
HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension
HINDSIGHT_API_TENANT_API_KEY=<minted bearer>
HINDSIGHT_CP_DATAPLANE_API_KEY=<the same bearer>
HINDSIGHT_CP_ACCESS_KEY=<minted access key>
HINDSIGHT_API_LLM_PROVIDER=openai
HINDSIGHT_API_LLM_BASE_URL=http://apps-gateway:4000/v1
HINDSIGHT_API_LLM_API_KEY=<the instance's key>
HINDSIGHT_API_LLM_MODEL=gpt-4o-mini
```

Two rules in the renderer are load-bearing and both came from a measurement.

**The LLM block is always written.** The image's own default provider is OpenAI
with a hosted key. An install whose operator configured nothing must say so in its
own configuration rather than dial a provider nobody chose, so with no inference
binding the file says `HINDSIGHT_API_LLM_PROVIDER=none`. That is a supported mode,
not a broken one: the server reaches healthy, retain stores the text as a chunk,
and semantic recall works. What is lost is the LLM half, and Hindsight raises a
clear error on those calls instead of a connection failure.

**A keyless endpoint still gets a non-empty key.** The app builds an OpenAI SDK
client, and that client refuses to construct with no key at all, while the app
collapses an empty string to "no key" on the way through. So an endpoint with no
credential is given the literal `placeholder`, which is obviously not a secret and
is ignored by the endpoint that receives it. `apps/podly` reached the same answer
from the same wall.

## A hazard that is documented rather than fixed

Hindsight takes a PostgreSQL advisory lock while it migrates. If the API container
is killed hard while holding it, the backend that owns the lock stays alive inside
PostgreSQL until TCP gives up on it, and the next API start waits for that lock
indefinitely, logging a warning every 30 seconds and never proceeding. Measured:
after a `podman rm -f` mid-migration, the successor was still waiting five minutes
later.

This does not happen on Bloud's normal paths. A graceful stop closes the
connection and the lock goes with it, and a reboot restarts PostgreSQL too, so
there is no stale backend to wait for. The window is a hard kill (an OOM kill is
the realistic one for a 2.6 GB image on a home server) landing in the roughly
fifteen seconds the first-boot migration takes.

The fix would be to make PostgreSQL notice dead clients sooner, with
`tcp_keepalives_idle`. That was tried and is not shipped: with the flags passed on
the postgres command line, `pg_settings` reported `source = command line` and
`setting = 0` for all three, so the mitigation could not be shown to work here.
Shipping a mitigation that cannot be verified is worse than recording the hazard.

The operator remedy is `podman restart` of the postgres container, which drops the
stale backend and lets the API proceed on its next pass.

## Trade-offs

**The full image, not `-slim`.** The slim build is 488 MB against 928 MB and has no
local embedding or reranking model, so it needs an embedding endpoint. The
`inference` contract promises completions from an OpenAI-compatible endpoint and
says nothing about embeddings, and an install that silently required a second
capability the contract does not carry would fail in a way the operator cannot fix
from Settings. The extra 440 MB buys an app that comes up with no endpoint
configured at all.

**The MCP endpoint is the only machine surface.** Hindsight also has a REST API,
and the gate does not route it. The catalog declares the `mcp` contract, so that is
the surface Bloud promises and can test. Publishing a second, differently
authenticated API on the same subdomain would be reach the app did not declare.

**`inference` is optional.** Only the operator can fill in an AI endpoint, and an
install that sat in ERROR waiting on a credential only they can supply would blame
them for a thing they have not been asked about yet. The app comes up useful and
gets better when the setting is filled in, and the reset on save requeues this
consumer, so filling it in reaches the running container.

**The gate is a third container and 55 MB.** The alternative, `extraPorts`, would
publish the memory API on the host so Traefik could dial it. That is a second
front door on the LAN for an API whose only intended caller is the gate.

## Icon

selfh.st/icons has no hindsight entry. The catalog's own rule is that an app the
set does not cover keeps its upstream icon, so `icon.png` is
`hindsight-favicon.png` from the root of `github.com/vectorize-io/hindsight` at
`main`, committed byte for byte (sha256
`6a9f82908ded489a9b86777e9bde54efb3652384abf87dc299616b45fdc915cb`). It is 186x139
rather than the 512 square the other tiles use, because it is what upstream
publishes and resizing it would be a recompression of someone else's artwork. The
other asset in that repository, `hindsight-control-plane/public/logo.png`, is a
529x139 wordmark and would not read as a tile.

## Validation strategy

Unit tests in `apps/hindsight` cover what a live install cannot cheaply pin:

- `configurator_test.go`: both credentials minted once and published, the Control
  Plane carrying the same bearer the API checks, the `none` provider selected when
  no inference binding exists, the binding reaching the file when one does, the
  keyless-endpoint placeholder, no env line ever written empty, rotation reaching
  the container, and idempotency with and without a secrets store.
- `gate_test.go`: the gate command and the mount path bound to `metadata.yaml`,
  only the gate publishing a port, the MCP regex claiming exactly the paths it
  should, each half locking independently, and the two upstream ports staying
  distinct.
- `api_test.go`: the handshake parser against the captured real response, and every
  shape that is "something is listening" but not "this app serves its tools".

Both nodes are rows in `apps/conformance_test.go`, so the shared idempotency and
nil-deps harnesses run against them.

The catalog rules that bind on this app are already enforced by existing tests:
`TestShippedCatalog_EveryMCPProviderIsHarnessReady` requires every `mcp` provider
to be declared by a shipped harness, which is why Hermes lists it, and
`TestDeclaredProviders_RealCatalogDeclaresEveryMcpProvider` pins the resulting
order.

No Playwright spec and no integration leg, the same call `apps/ha-mcp` made for the
same reason: the sign-in is a client-side form posting a minted key, and the
journey a browser could prove is shorter than the unit coverage of the same
conditions. The live-verification table above is the substitute, and it is the one
part of this document a future reader cannot reproduce without pulling 1.1 GB.
