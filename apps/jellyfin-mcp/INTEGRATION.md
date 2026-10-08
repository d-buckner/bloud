# jellyfin-mcp Integration

## Status: Working (live-verified)

The catalog ID is `jellyfin-mcp`, named after the upstream package it wraps:
[`Knuckles-Team/jellyfin-mcp`](https://github.com/Knuckles-Team/jellyfin-mcp),
image `docker.io/knucklessg1/jellyfin-mcp:mcp` pinned by digest
(`sha256:fc371dc6…43a2`). Inside the container the package reports 1.0.1 and
its MCP layer reports 3.4.4 (`fastmcp`), which is the version string the
server returns in `serverInfo`.

It gives an agent the media library Jellyfin already serves, through three
MCP tools:

| Tool | Reach |
|---|---|
| `jellyfin_media` | playback, streaming URLs, artists, playlists, audio/video queries |
| `jellyfin_library` | searches, items, collections, catalog queries |
| `jellyfin_system` | system info, configuration, users, devices, keys, backups, logs |

The surface is **action-routed**, not one tool per endpoint. Each tool takes an
`action` string plus a `params_json` object, so the whole catalog is three
tools rather than the ~90 methods the upstream client exposes. That is a real
trade-off: it keeps the model's tool list small, and it means a wrong `action`
is a runtime miss rather than a schema rejection.

## Why a dedicated API key instead of the admin password

The wrapper is not a browser and cannot join the identity provider, so it
needs a credential of its own. The obvious one, the `adminPassword` the
`mediaServer` contract publishes, does not work:

**The image cannot authenticate with a username and password at all.** Its
client sets `X-Emby-Token` from `JELLYFIN_API_KEY` and then probes
`/System/Info`. `JELLYFIN_USERNAME` and `JELLYFIN_PASSWORD` are read into
fields nothing logs in with. Measured against the pinned image with a
credentials-only config: every tool call ends in `AuthError: Jellyfin
authentication failed`. So the API key is not a preference, it is the only
credential shape this wrapper accepts.

Minting a key is also the better boundary independent of that. The key shows
up in Jellyfin's own Dashboard → Security → API Keys under the name
`jellyfin-mcp`, so an operator can revoke the agent without rotating the
admin password, and the admin password never enters this container's
environment. The configurator uses the bootstrap admin credential for
exactly one thing: the login call that creates the key.

The consequence is worth stating plainly: the tools see what **that key** can
see, which is a full-privilege Jellyfin key. `jellyfin_system` can read users
and configuration and can create and revoke other keys. That is the same
privilege level the bootstrap admin has; the difference is that it is
separately named and separately revocable.

## The mint: lookup before create

`PreStart` mints the key through Jellyfin's own API:

1. `POST /Users/AuthenticateByName` with the MediaBrowser identity header
   and the bootstrap admin's credential → a session token
2. `GET /auth/keys` → the server's whole key table
3. if no entry is named `jellyfin-mcp`, `POST /auth/keys?app=jellyfin-mcp`
4. re-read the table and take the value

Step 2 is not optional. `POST /auth/keys` **appends** a new key every call,
so a configurator that minted without looking would stack identical keys in
the Security screen on every Bloud database reset that re-ran the pass.
Adopting the existing key is also what makes a reinstall reuse the credential
the first install created.

Two details verified against Jellyfin 10.11.11 that the API docs do not make
obvious:

- The key name is a **query parameter** (`?app=`), not a request body. A
  body is rejected with a validation error naming a field that is not in it.
- A freshly created key reports `IsActive: false` in the listing and still
  authenticates. Confirmed by contrast: a bogus key gets 401 and the real one
  gets 200. Do not gate on `IsActive`.

The mint is cached in a private app secret (`jellyfinApiKey`), so a steady
state does no Jellyfin traffic at all: measured, the second `PreStart` pass
makes zero logins and zero creates.

## The inbound bearer is a JWT Bloud mints

The listener is not open. `AUTH_TYPE=jwt` makes the server verify a bearer
against an HMAC key it is configured with, so Bloud generates that key
(`FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY`, a private app secret) and mints the
token it signs, published as the `mcp` contract's `httpToken`.

Three of the four `jwt` modes upstream ships are unusable here, which is why
the config looks over-specified:

| `AUTH_TYPE` | What it actually does |
|---|---|
| `none` | `/mcp` is open to whoever can reach the port |
| `static` | accepts two tokens hardcoded in the upstream package (`test-token`, `admin-token`) |
| `jwt` | verifies an HS256 bearer against a key this installation generated |

`static` is a published credential, not a secret: anyone who reads the
package can call the tools. `jwt` is the only mode where the bearer a harness
presents is checked against something this install owns. Measured: no bearer
401, wrong signature 401, **wrong audience 401**, correct bearer 200.

The token carries `iss=bloud`, `aud=jellyfin-mcp`, `sub=bloud:jellyfin-mcp`,
`iat`, and **no expiry**. That is deliberate. A harness caches the bearer it
registered with, so an expiring credential means a namespace that works
until the day it silently 401s, and fixing it needs a minter wired to the
registration lifecycle. What Bloud has instead of expiry is rotation: clear
`httpToken` and `jwtVerificationSecret` from the secrets store and the next
pass mints a fresh pair, which invalidates the old bearer at the listener.
The verifier accepts a token with no `exp` and no `scope`, which is what
makes the long-lived shape possible at all.

`iss` and `aud` are required to match the values in `metadata.yaml`. A token
signed with the right key for a different audience is refused, so the two
sides are pinned together by `TestIssuerAndAudienceMatchTheContainerSpec`.

## The generated env file

The image reads its whole configuration from process env and has no config
file. Three values cannot be rendered into the container spec, because they
are resolved bindings or generated secrets rather than static metadata:

- `JELLYFIN_URL`: the media server address, from the `mediaServer` contract
- `JELLYFIN_API_KEY`: the key this app mints
- `FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY`: the HMAC secret this app generates

So the configurator writes `{{appDataDir}}/config/env` and the container
declares it as `envFile`. The runtime reads that file on the host and merges
it into the podman config at create time; the file is not mounted into the
container. That is also why it is written `ModeHostOnly` rather than the
world-readable mode a mounted config file needs: nothing inside the container
ever opens it, and this file holds two live credentials.

Only values that actually resolved are written. An unresolved binding is an
absent variable, not an empty one. A later pass rewrites the file and
restarts the container once the binding lands.

## Why the readiness probe is the MCP handshake, with the bearer

The container's own `/health` returns 200 whenever the process is up. It
says nothing about Jellyfin, nothing about the MCP layer, and nothing about
whether authentication is being enforced. Promoting a node on that would
report a healthy app that cannot serve a single authorized MCP request.

So `PostStart` POSTs a real `initialize` to the same `/mcp` path a harness
calls and requires the server's own identity back, presenting the published
bearer. A status code cannot answer this: a gateway in front of a dead child
can answer 200 with the failure inside the JSON-RPC envelope, so only
reading the envelope separates "serving MCP" from "up, refusing".

`PostStart` also fails when no media server is bound. A node that reaches
`RUNNING` as a tool surface with nothing behind it is a worse report than one
that stays in `PRESTART` until Jellyfin arrives.

## Deviations from the wrapper norm, stated plainly

**One cached network call in `PreStart`.** The other MCP wrappers keep
`PreStart` offline. This one logs into Jellyfin and mints a key there, once,
and caches the result. The alternative was doing it in `PostStart`, which is
worse: the container is created from the env file, so the key has to exist
before the container starts, not after. The call is bounded, cached, and
skipped entirely when the key already exists, which is every pass after the
first. The conformance harness runs this app with no `mediaServer` binding,
which is the shape that keeps its own offline assertion true.

**`headless: true`.** There is no page for a tile to open. The app is
installed, reconciled, and routed like any other; the dashboard grid just
leaves it out.

## Limitations

- **amd64 only.** Upstream's container pipeline builds no arm64 leg. On an
  ARM host this app will not pull. Nothing in Bloud detects that at install
  time, so the failure surfaces as a container that never becomes healthy.
- **No version tags upstream.** The only tags on this image are `latest`,
  `main`, `mcp`, and `sha-<commit>`, all republished. The digest pin is
  therefore load-bearing rather than belt-and-braces: it is the only
  reference that means the same bytes twice. A bump means resolving a new
  digest by hand; `npm run check:image-pins` will not do it for you.
- **Action-routed tools make bad calls invisible until runtime.** `tools/list`
  says three tools; the real method list is a string in a description field.
- **The key is full-privilege.** Revoking it is the control, and it lives in
  Jellyfin's UI, not in Bloud's.

## Verification

Installed live through the host-agent API on the native dev stack (the real
dependency-graph path), against Jellyfin 10.11.11:

- `jellyfin` converged first, then `jellyfin-mcp`: `planning → health →
  running/complete` in ~24s, with the declared `mediaServer` edge ordering
  Jellyfin's convergence ahead of the key mint
- the install response carries `"headless":true`, so the dashboard draws no
  tile while the app stays installed, reconciled, and routed
- `{{appDataDir}}/config/env` written `0600` with all three values resolved:
  `JELLYFIN_URL='http://apps-jellyfin:8096'`, the minted `JELLYFIN_API_KEY`,
  and `FASTMCP_SERVER_AUTH_JWT_PUBLIC_KEY`
- Jellyfin's own key table holds exactly one key, named `jellyfin-mcp`, and
  its value matches the stored `jellyfinApiKey`. It reports `IsActive: false`
  and authenticates
- through Traefik on `jellyfin-mcp.localhost:8080`: no bearer 401, the
  published `httpToken` 200, `tools/list` returns the three tools, and
  `jellyfin_system / get_system_info` returns the real `SystemInfo` for
  `Jellyfin Server 10.11.11` in 49 ms
- the ~60s config resync ran `PreStart` with `restart_needed: false` and
  `PostStart` in 7 ms: a read-only diff that disturbed no container and made
  no Jellyfin call, because the key was cached
- uninstall removed both apps cleanly; the stack returned to its prior set

Against the pinned image directly, with the same env this configurator
produces:

- no bearer 401, wrong signature 401, **correct key with the wrong audience
  401**, correct bearer 200 with `serverInfo.name = "jellyfin-mcp MCP"`
- `jellyfin_system / get_users`: real user list, `isError: false`, 28 ms
- a second mint pass adopts the existing key: 0 creates

## Icon

selfh.st/icons has no `jellyfin-mcp` entry, so the app ships
`icon.png` copied verbatim from the set's Jellyfin icon
(`https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/jellyfin.png`, 512×512
RGBA, unmodified). It is the media server's mark rather than the wrapper's
own, which is the honest reading: this app is a door into that library.
