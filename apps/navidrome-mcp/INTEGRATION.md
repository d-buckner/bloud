# navidrome-mcp Integration

## Status: implemented, not yet exercised end to end

The configurator, the contract resolver, and the catalog wiring have unit and
resolver tests. No install has run against a live Navidrome yet, so the claims
below about what the image does come from upstream's own documentation and its
published image, not from a boot.

Upstream: [Blakeem/Navidrome-MCP](https://github.com/Blakeem/Navidrome-MCP),
AGPL-3.0. Image: `ghcr.io/blakeem/navidrome-mcp`, pinned to 2.6.0 by tag and by
digest.

## What this app is

A wrapper. It ships no music server. It puts an MCP tool surface in front of a
Navidrome that already exists, using the same account a Subsonic client would
use.

The wrapper shape is the one `docs/features/mcp.md` describes: an ordinary
catalog app that provides the `mcp` contract and consumes the contract of its
target. Hermes installs the namespace by declaring the provider, so nothing in
the harness changes when this app appears.

## The credential: `mediaServer`, spent on every call

The wrapper needs three things: an address, an account name, and that account's
password. That is what the `mediaServer` contract carries, so it reuses that
contract instead of adding one.

What the contract promises is the login. It says nothing about how the consumer
spends it, and most consumers trade it: Jellyfin hands out a long-lived API key,
Home Assistant a long-lived token, and the wrapper caches what it minted. That
step is a helper (`apps/jellyfin`'s `EnsureAPIKey`), not a term of the contract.
Navidrome speaks the Subsonic protocol, which signs each request with the password
itself and has no key to mint, so this consumer keeps the account and holds
nothing it could revoke.

What decides which providers arrive is the consumer's `compatible:` list, not the
contract name. A household runs Jellyfin for the TV and Navidrome for the music,
and this app names only Navidrome.

For a Bloud-booted Navidrome the two fields are the internal `bloud-admin` account
and the password the host generated for it. That is the only account password this
instance holds: every other Navidrome account is synced from Authentik, and Bloud
never learns a password for one.

## Local or external, one binding

`apps/navidrome` now provides `mediaServer`. That one declaration makes both
shapes work, and the wrapper has no branch between them.

**Local.** Install `navidrome-mcp` and the graph orders Navidrome first, because
the integration is required. The binding carries `http://apps-navidrome:4533` and
the bootstrap admin credential.

**External.** An operator who already runs Navidrome registers it as an external
app with source `app:navidrome`. The registration form is generated from
`apps/navidrome/metadata.yaml`, so it asks for an endpoint, an account password,
and an account name. The `operatorValues` entry on `adminUsername` is what makes
the form ask for the name instead of prefilling `bloud-admin`, a name nothing out
there owns.

From then on the resolver hands the wrapper the operator's origin and the operator's
credential, and `PreStart` writes them into the same env file. The consumer never
learns which kind it got. `TestExternalNavidromeFillsNavidromeMCPBindingFromTheRealCatalog`
in
`services/host-agent/internal/engine/orchestrator/external_app_resolution_test.go`
pins that against the two real metadata files, and
`TestInstallConsumerDoesNotInstallARemoteProviderLocally` is why registering the
remote Navidrome first stops Bloud from also installing a local one.

One consequence of the required integration: with no external record registered,
installing `navidrome-mcp` installs Navidrome with it. That is the declared
default provider, and it is what makes the app useful with no setup. Register the
external Navidrome before installing the wrapper to keep Bloud from adding a
container you already run somewhere else.

## The credential reaches Navidrome through `/rest/`

`apps/navidrome` runs behind Authentik forward-auth, with `/rest/` in
`bypassPaths` so Subsonic clients can authenticate on their own. The wrapper
calls `${url}/rest/...` and nothing else, so it travels through that bypass and
authenticates with the account password like every other Subsonic client.

That is also why an account whose password lives only in Authentik cannot be used
here. The Subsonic protocol needs the password to compute each request's token.
The `secretHelp` text on the offer says so on the form where the operator would
otherwise guess.

## The generated env file

`PreStart` writes `{{appDataDir}}/config/env`, and the container spec declares it
as `envFile`. The runtime reads it on the host and passes the values in at create
time, so the file is never mounted and carries `ModeHostOnly` (0600). It holds the
music server's account password.

| Variable | Where it comes from |
| --- | --- |
| `NAVIDROME_URL` | the binding's `BaseURL`: a container name locally, the operator's origin externally |
| `NAVIDROME_USERNAME` | the binding's `AdminUsername` |
| `NAVIDROME_PASSWORD` | the binding's `AdminPassword` |
| `MCP_HTTP_AUTH_TOKEN` | the bearer this app mints and publishes under `provides.mcp` |

Only values that resolved are written. An unresolved provider leaves the variable
absent rather than empty, because the server reads those two differently: an
absent `NAVIDROME_URL` is a configuration error it reports, an empty one is a
destination it will try to dial. A provider that is installed but has not published
a credential yet gets a comment line in the file, so "not ready" is a state the
next pass can see.

Values are written bare, with no quoting. The reader is the runtime's env-file
parser: it trims the line, cuts on the first `=`, then strips one layer of
matching surrounding quotes. A bare value comes back unchanged unless it carries a
line break, arrives with padding the trim would eat, or happens to begin and end
with the same quote character. `envValue` refuses exactly those, and a refused
value is left out of the file with a comment naming the key and never its value.

## Why there is no gate container

`apps/ha-mcp` ships an nginx front because its image has no notion of a bearer and
treats a secret URL path as the credential. This image is the other shape: it
compares `MCP_HTTP_AUTH_TOKEN` in constant time and answers 401 to anything else.
The bearer the `mcp` contract promises is enforced by the thing it was given, so
one container is enough and the published port has no way around the check.

The transport binds `0.0.0.0` because a published port needs a listener that does
not bind loopback. Upstream turns off its automatic `Host` header filter once a
bearer is set, which is the right call here: a remote client reaches this
container by names the container cannot know in advance, and a lured browser cannot
attach the bearer.

## What this app does not get: audio

The published image installs no mpv. The 20 playback tools are absent from
`tools/list`, and the MPV Remote web UI never starts. Search, playlists, ratings,
listening analytics, radio, and lyrics all work.

Upstream is explicit that a container is the library-only shape and that adding
mpv to an image is rarely what you want: audio still has to reach a real sound
device, which needs `/dev/snd` or a PipeWire socket on a Linux host, and does not
work at all under Docker Desktop. Bloud ships no images of its own, so a derived
image is not on the table. `WEBUI_ENABLED=false` is set so the player's routes are
not added at all.

An operator who wants playback from the assistant runs the server on the machine
wired to speakers, outside Bloud, and points MCP clients at it over the HTTP
transport. Bloud's job there is the Navidrome credential, not the player.

## Optional features: two on, one off

| Feature | What it needs | Bloud sets it |
| --- | --- | --- |
| LRCLIB lyrics | a user-agent string, no key | yes: `LYRICS_PROVIDER=lrclib` plus `LRCLIB_USER_AGENT` |
| Radio Browser discovery | a user-agent string, no key | yes: `RADIO_BROWSER_USER_AGENT` |
| Last.fm discovery | an API key of the operator's | no |

Last.fm stays off because the key is the operator's and no instance setting holds
one. Adding that setting is a wider change than the app, and the feature degrades
to absent rather than broken.

## Readiness: the probe is the handshake

The container health check is the image's own `/healthz`, restated on port 4534.
It is liveness only and makes no Navidrome call by design, so it answers 200 for a
server that cannot reach its library. Its retries are generous because the image
binds its socket only after it has signed in to Navidrome and primed its caches.

`PostStart` is where the wiring is proven: an MCP `initialize` against
`http://localhost:4534/mcp` with the published bearer, accepted only when the
result names a server. A 401 means the bearer did not reach the container, and a
server that answers the handshake is serving MCP for someone.

## Trade-offs

- No audio, and no path to it without Bloud building an image.
- One Navidrome account is shared by every consumer of the namespace, so
  `set_active_libraries` changes the library filter for every session. Upstream
  states this for its HTTP transport.
- The published credential is an admin account on a Bloud-booted Navidrome. It is
  revocable only by rotating that password, which affects the instance's own
  tooling.
- The JSON-RPC-over-SSE response parser is the third copy in the catalog, after
  `apps/ha-mcp` and `apps/arr-mcp`. Extracting one shared helper is a change that
  belongs to all three at once.

## Verification

Run and passing:

```bash
cd apps && go test ./navidrome-mcp/ ./navidrome/ .
cd services/host-agent && go test ./internal/catalog/ ./internal/engine/orchestrator/
```

What they pin:

| Test | The claim |
| --- | --- |
| `TestPreStartWritesResolvedBindings` | the binding and the bearer land in the env file, and a second pass changes nothing |
| `TestPreStartProviderWithoutCredentialSaysSo` | "not ready" is written as an absent variable plus a comment, never as an empty password |
| `TestPreStartFileModes` | the env file is 0600 and the mounted directory is writable by the image's uid |
| `TestEnvValue` | what survives one `KEY=value` line unchanged |
| `TestBearerMintedOnce` | the bearer is minted once and read back forever |
| `TestMetadataPathMatchesPublishedContractValue` | the probed path and the published `path` value are one constant |
| `TestExternalNavidromeFillsNavidromeMCPBindingFromTheRealCatalog` | a registered remote Navidrome reaches the wrapper with the operator's address and account |
| `TestLocalNavidromeFillsNavidromeMCPBindingFromTheRealCatalog` | with no external record the same declaration binds the local container |
| `TestConformance/apps-navidrome-mcp` | the shared offline harness: name, port, idempotent `PreStart`, nil-deps safety |

Not verified: a real install, a real Navidrome sign-in, and a real tool call.
Those need the integration tier with a Navidrome group.

## Icon

`selfh.st/icons` has no `navidrome-mcp` entry, and the app is `headless`, so there
is no tile to draw an icon on. No `icon.png` ships, which is the same treatment
`apps/ha-mcp` and `apps/dav-mcp` get.

## Validation strategy

- Unit: `apps/navidrome-mcp/configurator_test.go`.
- Resolver: the two `...Navidrome...BindingFromTheRealCatalog` cases in
  `external_app_resolution_test.go`, which load the checked-in catalog rather
  than a fixture.
- Conformance: `apps/conformance_test.go` runs the shared harness with a wired
  `mediaServer` provider.
- A live install would assert the behavioral outcome through the MCP endpoint:
  `tools/list` answers the library tools, and `test_connection` reports a signed-in
  Navidrome. That belongs in the integration tier once this app gets a group there.
