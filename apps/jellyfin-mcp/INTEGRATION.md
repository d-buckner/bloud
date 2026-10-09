# jellyfin-mcp Integration

## Status: Working (live-verified against an off-host Jellyfin 12.1.0)

The catalog ID is `jellyfin-mcp`, named after the upstream it wraps:
[`jaredtrent/jellyfin-mcp`](https://github.com/jaredtrent/jellyfin-mcp), image
`ghcr.io/jaredtrent/jellyfin-mcp:2026.924.1` pinned by digest as well as by tag
(`sha256:47877d8c…ab2a0`).

It gives an agent the media library Jellyfin already serves. The image ships 31
tools, grouped by what they touch (`jellyfin_search`, `jellyfin_browse`,
`jellyfin_play`, `jellyfin_playlists`, `jellyfin_metadata`, `jellyfin_users`,
`jellyfin_system_info`, and the rest), selected at startup by `--toolsets` if an
operator ever wants a narrower surface. Bloud runs the default set with
`--disable-destructive`.

This replaced an earlier wrapper over `Knuckles-Team/jellyfin-mcp`, which was
broken against Jellyfin 12 for a reason worth keeping on the record; see "Why
this upstream" below.

## Why this upstream

The wrapper it replaced authenticated with `X-Emby-Token`. Jellyfin 12 ships
with `EnableLegacyAuthorization` off by default, so that header is simply
ignored: every tool call failed with `Failed to resolve dependency 'client'`
against a Jellyfin 12 server, while the same image worked against 10.11. That
is not a bug Bloud could configure around, because the header is set in the
package's client constructor.

`jaredtrent/jellyfin-mcp` gets it right, and its own release notes for the
pinned version are the changelog entry: "Jellyfin 12 support: the API key
travels in the Authorization header". Verified against the image rather than
against its README:

| Check | Result |
|---|---|
| Auth header it sends | `Authorization: MediaBrowser …`, accepted by Jellyfin 12 |
| Wrong bearer at the MCP listener | `401 Unauthorized` |
| Multi-arch | `linux/amd64` and `linux/arm64` in the index |
| Base image, size, user | Alpine 3.24, 23.8 MB, non-root `jellyfin` |
| License | MIT |
| Endpoints | `/mcp` (streamable HTTP), `/health` (unauthenticated) |

It is also the only candidate that is both actively maintained and correct for
the server Bloud actually ships against.

## Why a dedicated API key instead of the admin password

The wrapper is not a browser and cannot join the identity provider, so it needs
a credential of its own. The image reads `JELLYFIN_URL`, `JELLYFIN_API_KEY`, and
an optional `JELLYFIN_USER_ID`, and has no username-and-password login at all:
there is no credential shape but the key.

Minting one is also the better boundary. The key shows up in Jellyfin's own
Dashboard -> Security -> API Keys under the name `jellyfin-mcp`, so an operator
can revoke the agent without rotating the admin password, and the admin password
never enters this container's environment. The configurator uses the bootstrap
admin credential for exactly one thing: the login call that creates the key.

The consequence is worth stating plainly: the tools see what **that key** can
see, which is a full-privilege Jellyfin key. `--disable-destructive` is the
layer that stands between "the agent may manage the library" and "the agent may
empty it": the wrapper will write metadata, libraries, and playlists, and refuses
deletes, restarts, and revocations. The privilege and the guardrail are separate
controls on purpose, and the guardrail is a flag, not a sandbox.

## Which account it logs into

The account the mint uses is read off the `mediaServer` binding
(`MediaServerBinding.AdminUsername`), never hardcoded. That is what makes a
Jellyfin Bloud did not boot work as the provider:

| Provider | `AdminUsername` on the binding | Where it comes from |
|---|---|---|
| Jellyfin installed by Bloud | `bloud-bootstrap-admin` | the static `provides.mediaServer.values.adminUsername` in `apps/jellyfin/metadata.yaml`, the account `apps/jellyfin` creates |
| Jellyfin registered as an external app | whatever the operator typed | the external record's `values.mediaServer.adminUsername`, surfaced by the generated form in Settings |

`apps/seerr` reads the same field for the same reason. An earlier shape of this
configurator used a local constant, which made the remote case fail
unconditionally: the login 401s against a server whose admin is called anything
else, `PreStart` fails, and the node never converges. Pinned by
`TestPreStartLogsInAsTheAccountTheProviderPublished` and
`TestPreStartLogsInAsTheBootstrapAccountForALocalProvider`.

A binding that carries a password and no username fails the pass rather than
guessing (`TestPreStartFailsWhenTheProviderPublishesNoAdminUsername`), and the
failure names the account it was refused on.

### Registering a remote Jellyfin

Settings -> External apps -> Remote app -> Jellyfin. The form is generated from
the contract registry, so it asks for the endpoint origin and the `mediaServer`
pair (`adminUsername`, `adminPassword`) and nothing else. Installing
`jellyfin-mcp` after that does not pull a local Jellyfin: the install planner
skips a required provider the external registry already answers
(`orchestrator/pipeline.go`), and the wrapper binds the remote origin for both
the mint and the container's `JELLYFIN_URL`.

Two things the remote shape changes, neither of them invisible:

- **The mint writes to someone else's server.** The key named `jellyfin-mcp`
  lands in that instance's Dashboard -> Security -> API Keys, so the account
  supplied has to be an administrator there.
- **TLS is verified with the system trust store.** `pkg/appclient` sets no custom
  root and no `InsecureSkipVerify`, so an `https://` remote on a self-signed
  certificate fails the handshake. Use an origin a system CA covers, or run the
  remote over plain http on the LAN.

## The mint: lookup before create

`PreStart` mints the key through Jellyfin's own API:

1. `POST /Users/AuthenticateByName` with the MediaBrowser identity header and
   the bootstrap admin's credential -> a session token
2. `GET /auth/keys` -> the server's whole key table
3. if no entry is named `jellyfin-mcp`, `POST /auth/keys?app=jellyfin-mcp`
4. re-read the table and take the value

Step 2 is not optional. `POST /auth/keys` **appends** a new key every call, so a
configurator that minted without looking would stack identical keys in the
Security screen on every Bloud database reset that re-ran the pass. Adopting the
existing key is also what makes a reinstall reuse the credential the first
install created.

Two details about Jellyfin's API that the docs do not make obvious:

- The key name is a **query parameter** (`?app=`), not a request body. A body is
  rejected with a validation error naming a field that is not in it.
- A freshly created key reports `IsActive: false` in the listing and still
  authenticates. Do not gate on `IsActive`.

The mint is cached in a private app secret (`jellyfinApiKey`), so a steady state
does no Jellyfin traffic at all: measured, the second `PreStart` pass makes zero
logins and zero creates.

## The inbound bearer is opaque, and Bloud owns it

The listener is not open. The image refuses to bind a non-localhost address
without a token:

```
FATAL: an HTTP token (--http-token or HTTP_TOKEN) is required when listening
on non-localhost address 0.0.0.0:8080   ->   Exited (1)
```

So there is no "run it open and let the proxy decide" variant, which is the
right default for a tool server that can delete a library. Bloud generates the
token (32 bytes of entropy, unpadded base64url) and publishes it as the `mcp`
contract's `httpToken`, so the credential a harness presents is checked by the
thing it was given rather than being a name that authenticates against nothing.

It is an opaque string rather than a JWT because that is what the listener
checks: an exact comparison, with no signature, audience, or expiry to verify.
The wrapper this replaced had a `jwt` auth mode, which is why an earlier shape of
this app minted and signed HS256 tokens; that machinery is gone, and with it the
`jwtVerificationSecret` app secret it used to write. A store written by that
older shape keeps the retired key as inert data: nothing reads it, and nothing
hands it out, because consumers receive only the secret names they declare.

The bearer carries no expiry, for the same reason it carries no signature. A
harness caches the bearer it registered with, so an expiring credential is a
namespace that works until the day it silently 401s. What Bloud has instead of
expiry is rotation: clear `httpToken` from the secrets store and the next pass
generates a fresh bearer, which invalidates the old one at the listener.

The token reaches the process through the env file rather than the
`--http-token` flag deliberately: a flag would put the credential in the process
list, where any local process can read it.

## The generated env file

The image reads its whole configuration from process env and has no config file.
Three values cannot be rendered into the container spec, because they are
resolved bindings, a provider round trip, or a generated secret:

- `JELLYFIN_URL`: the media server address, from the `mediaServer` contract
- `JELLYFIN_API_KEY`: the key this app mints
- `HTTP_TOKEN`: the bearer this app's own listener requires

So the configurator writes `{{appDataDir}}/config/env` and the container
declares it as `envFile`. The runtime reads that file on the host and merges it
into the podman config at create time; the file is not mounted into the
container. That is also why it is written `ModeHostOnly` rather than the
world-readable mode a mounted config file needs: nothing inside the container
ever opens it, and this file holds two live credentials.

Only values that actually resolved are written. An unresolved binding is an
absent variable, not an empty one. The one exception is the bearer: it needs
nothing from the outside world, so it is always present, because "no media server
yet" is not a reason to start an unauthenticated server. A later pass rewrites
the file and restarts the container once a binding lands.

## The readiness probe is a real tool call, not a handshake

Two probes run in `PostStart`, in this order.

**First the handshake, with the bearer.** A `POST /mcp` `initialize` to the same
endpoint a harness calls, presenting the published bearer, requiring the
server's own identity back. This is what separates "the process is up" from "the
listener accepts the credential Bloud published". `/health` cannot answer
either: it returns 200 as long as the process is alive.

**Then one real tool call through that session.** `tools/call jellyfin_system_info
{"action": "info"}`, which is a read of `/System/Info` made by the wrapper's own
client with the wrapper's own key, and the answer must be a result and must not
be an error.

The second probe is the one that matters, and its absence is the reason this app
was rewritten. The wrapper answers `/health`, starts cleanly, and completes the
MCP handshake **whether or not Jellyfin accepts its key**: at startup it reads
the unauthenticated `/System/Info/Public` and logs `Connected to Jellyfin
12.1.0` either way. Measured with a deliberately wrong `JELLYFIN_API_KEY`, the
container came up healthy, `/health` answered 200, the handshake succeeded, and
every tool call returned:

```json
{"result":{"content":[{"type":"text","text":"Jellyfin API error: API error 401
 (Jellyfin rejected the API key; check JELLYFIN_API_KEY)"}],"isError":true}}
```

A handshake-only gate therefore reports a healthy, serving node in exactly the
situation that started this work: a credential the provider refuses. The tool
call is the only probe in this app that can fail for a reason on the provider's
side, and `isError` is the field that carries it. Reading the status code, or
even the absence of a JSON-RPC `error`, both miss it, because the refusal is a
well-formed 200 result.

The session is why the probe is three requests rather than one. The server hands
its session id back in the `Mcp-Session-Id` **response header** and refuses every
later request that does not carry it (`method "tools/call" is invalid during
session initialization`), so the probe has to read a header. `pkg/appclient`
never exposed response headers, so it gained `CaptureHeader` for exactly this:
narrow, additive, and stdlib-only, with its own tests.

## How a broken provider surfaces

The probe's failure is reported differently depending on which pass ran it, and
that difference is the engine's, not this app's:

- **On the initial lifecycle drive**, a `PostStart` failure stops the node from
  reaching `RUNNING`. An install whose provider is broken does not complete.
- **On a resync pass**, a `PostStart` failure is a logged diff: the node stays
  `RUNNING`, the pass logs `resync: PostStart failed` at WARN, and the app's
  operation row records a retryable failure at the `poststart` phase with the
  cause (`orchestrator/execution.go:runResyncPostStart`, pinned by
  `TestOrchestrator_Resync_FailureLeavesNodeRunning`). The next pass that
  succeeds clears it.

So a key revoked in Jellyfin's UI after the install converged does not flip the
tile red. It raises a WARN and a failed operation on the app, and it is caught
within one self-healing interval (~60s) rather than at the next install. Leaving
the node `RUNNING` is deliberate: the container really is serving, and flipping a
serving app to terminal ERROR because its provider is unhappy is a worse report
than an accurate one.

Because `PostStart` runs on every resync, the probe is also what makes the
periodic pass a real diff against the outside world (invariant 2) instead of a
no-op: a provider that stops answering is noticed while a human is still
looking.

## Deviations from the wrapper norm, stated plainly

**One cached network call in `PreStart`.** The other MCP wrappers keep `PreStart`
offline. This one logs into Jellyfin and mints a key there, once, and caches the
result. The alternative was doing it in `PostStart`, which is worse: the
container is created from the env file, so the key has to exist before the
container starts, not after. The call is bounded, cached, and skipped entirely
when the key already exists, which is every pass after the first. The
conformance harness runs this app with no `mediaServer` binding, which is the
shape that keeps its own offline assertion true.

**`headless: true`.** There is no page for a tile to open. The app is installed,
reconciled, and routed like any other; the dashboard grid just leaves it out.

**The container listens on the catalog port.** `--addr 0.0.0.0:9334` replaces the
image's own `--addr 0.0.0.0:8080`, so the container port, the published port,
and the catalog `port` are one number with no translation to keep straight.
Traefik routes to the host port, so all three have to agree.

## Limitations

- **The stored key is never revalidated.** `ensureJellyfinAPIKey` returns the
  cached value without asking the server whether it still works. A key revoked
  in Jellyfin's UI therefore survives in the store, and the only thing that
  notices is the `PostStart` probe, which reports it as a WARN and a failed
  operation rather than repairing it. Clearing `jellyfinApiKey` from the secrets
  store is the manual repair; the next pass mints a fresh key.
- **A steady-state resync makes one provider call.** The handshake plus the tool
  call are two requests to the wrapper and one to Jellyfin, every ~60s. That is
  the price of a probe that can actually fail; the older handshake-only shape
  cost one request and bought nothing.
- **`isError` is the only signal.** A wrapper that reported a provider failure as
  a successful result with different wording would slip through. The predicate
  requires a non-empty result, so a silent empty answer is caught, but the
  contract is the upstream's to keep.
- **The key is full-privilege.** `--disable-destructive` is a policy inside the
  wrapper, not a permission boundary on the server. Revoking the key is the real
  control, and it lives in Jellyfin's UI, not in Bloud's.
- **Timezone is the container default (UTC).** The image reads `TZ`, and Bloud has
  no instance-wide timezone setting to offer it, so `activity_log` and
  `playback_history` timestamps come back in UTC.

## Verification

Installed live through the host-agent API on the native dev stack (the real
dependency-graph path), against an **off-host Jellyfin 12.1.0** registered in
Settings -> External apps (`https://media.thebloud.org`, server id
`13754c11…f34b`):

- the image swap was picked up by a config resync and the container was
  recreated onto `ghcr.io/jaredtrent/jellyfin-mcp@sha256:47877d8c…ab2a0` with
  `--http --addr 0.0.0.0:9334 --disable-destructive`
- `{{appDataDir}}/config/env` rewritten `0600` with all three values resolved:
  `JELLYFIN_URL='https://media.thebloud.org'`, the adopted `JELLYFIN_API_KEY`,
  and `HTTP_TOKEN`
- the mint adopted the key already on the remote server rather than creating a
  second one; the same key authenticates with `Authorization: MediaBrowser` and
  is refused with `X-Emby-Token`, which is the old wrapper's bug in one line
- the orchestrator's own resync logged `jellyfin-mcp is serving MCP and reaching
  Jellyfin through it`, i.e. the new `PostStart` ran the tool call and it
  returned real data
- `GET /api/apps/installed` reports `jellyfin-mcp` `running`, operation
  `reconcile/complete`
- through the MCP endpoint directly (`:9334`) **and through Traefik**
  (`jellyfin-mcp.localhost:8080/mcp`), with the published `httpToken`:
  `tools/list` returns 31 tools, and `jellyfin_system_info {"action":"info"}`
  returns `{"server_name":"bloud","version":"12.1.0","id":"13754c11…"}`
- no bearer or a wrong bearer at `/mcp`: `401 Unauthorized`
- the bearer was **not** rotated across the swap, so a harness that registered
  the `jellyfin-mcp` namespace against the old shape keeps working: the stored
  value is still the older HS256 string, and the new listener accepts it as the
  opaque secret it is
- the self-healing pass was observed repairing the container after it was
  stopped out from under the orchestrator, and the following resync passed again

Against the pinned image directly, with the same env this configurator produces:

- a correct key: `Connected to Jellyfin 12.1.0`, and `jellyfin_system_info`
  returns the real `SystemInfo`
- a wrong key: the container still starts, still logs that it connected, `/health`
  still answers 200, and the tool call returns `isError: true` with
  `Jellyfin API error: API error 401`. This is the measurement that made the
  handshake-only gate provably insufficient, and `TestPostStartFailsWhenTheMedia
  ServerRefusesTheKey` asserts this app's gate rejects it, including that a real
  tool call was the thing that failed
- without `HTTP_TOKEN` while bound to `0.0.0.0`: `Exited (1)` with the FATAL
  quoted above

## Icon

selfh.st/icons has no `jellyfin-mcp` entry, so the app ships `icon.png` copied
verbatim from the set's Jellyfin icon
(`https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/jellyfin.png`, 512x512
RGBA, unmodified). It is the media server's mark rather than the wrapper's own,
which is the honest reading: this app is a door into that library.
