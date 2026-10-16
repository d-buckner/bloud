# Podly Integration

## Status: implemented, smoke-tested against the lite image

`podly` is [Podly Pure Podcasts](https://github.com/podly-pure-podcasts/podly_pure_podcasts):
it downloads a podcast episode, transcribes it, finds the ad segments, silences
them, and republishes the result as an RSS feed a podcast player can subscribe
to. It is the first catalog app whose job is to rewrite media rather than serve
it.

- Image: `ghcr.io/podly-pure-podcasts/podly-pure-podcasts:2.7.7-lite` (pinned)
- SSO strategy: `none`. The human credential is a `clientPassword`
  (`reveal: once`, `rotate: bloud`)
- Port: `5001`, published to the host and routed by Traefik
- State: `{{appDataDir}}/instance` mounted at `/app/src/instance`
- Config: `{{appDataDir}}/config/env`, declared as the container's `envFile`
- Integration: `transcription`, optional, served by Settings rather than by an app

The `2.7.5-lite` image was run by hand (throwaway container, its own volume, no
Bloud runtime involved) with the env file this configurator writes, and every
behaviour the integration depends on was checked against it:

| Claim | What the real image did |
| --- | --- |
| The env file bootstraps the service admin | `bloud-service` existed and signed in: `200`, `role: admin` |
| Auth is on with `REQUIRE_AUTH=true` | `GET /api/auth/status` returned `{"require_auth":true}` |
| The health probe answers without a session | `urllib.request.urlopen("http://127.0.0.1:5001/")` succeeded inside the container |
| Accounts can be created and re-pointed after first boot | `POST /api/auth/users` `201`, duplicate `409`, `PATCH /api/auth/users/bloud` `200`; the old password then `401` and the new one `200` |
| The config API will not show the stored key | `GET /api/config` returns `api_key_preview: "sk-s...ey-1"`, first four and last four characters |
| A remote transcription endpoint can be configured | `PUT /api/config` `200`, and `base_url`, `model`, `whisper_type` round-tripped |
| The app's config survives a restart | the whisper section and both accounts were still there after a restart, so a digest-gated write does not have to re-deliver |
| A stable `PODLY_SECRET_KEY` keeps sessions across a restart | a cookie issued before the restart still authenticated: `200` |
| Rotating it ends every session | the same cookie returned `401` after the key changed, and signing in again worked |

What that does not cover is the orchestrator's own path: the install through the
API, the graph, and the dashboard. That is the integration tier's job, and it is
now a 0.45 GB pull rather than a 4.73 GB one.

## Why `none` is the honest strategy

Podly has exactly two credential paths, and neither one is a place a reverse
proxy can plug in:

1. A Flask session cookie, issued by a login form that checks a bcrypt hash
   stored in the app's own SQLite database.
2. A per-feed token pair (`feed_token` and `feed_secret`, query parameters) that
   a podcast player presents on the ad-free feed and audio URLs.

There is no OIDC client, no LDAP, no trusted-header mode, and no proxy-auth hook.
`sso.strategy` describes what the app does at the ingress, and the honest value
for an app that authenticates every request itself is `none`.

The tempting alternative is `forward-auth` with the feed paths bypassed, and it
is wrong for two reasons. The first is that it does not remove the app's own
login: forward-auth gates the browser, and the app would still demand a session
for anything the bypass does not cover, so the operator ends up with two
passwords. The second is what the bypass would leave behind. With
`REQUIRE_AUTH=false` the app serves `/feed/<id>` to anyone, and those ids are
sequential integers: the whole library becomes enumerable by counting. With
`REQUIRE_AUTH=true` the same paths require a feed token whose secret is 18
random bytes (`secrets.token_urlsafe(18)`, in `src/app/writer/actions/feeds.py`).

So Bloud turns the app's own authentication on and keeps it on. The feeds stay
reachable by a player, which cannot follow an SSO redirect, and they stay
protected by a credential the app mints per share link.

## Two accounts, because the environment can only speak once

Podly reads `PODLY_ADMIN_USERNAME` and `PODLY_ADMIN_PASSWORD` in
`bootstrap_admin_user`, and that function returns immediately if the users table
has any row in it. The environment is therefore a first-boot-only channel: it can
create one admin, and it can never change that admin's password again.

A Bloud rotation has to reach the app somehow, so the design splits the two
consumers of "a password for Podly":

- **`bloud-service`** is the account the environment creates. Its password is a
  host-held secret (`GenerateAppAdminPassword`), never revealed, and never
  rotated. It exists so Bloud always has an authenticated path into the app.
- **`bloud`** is the account a person signs in with. Its password is the
  `clientPassword` value Bloud mints, stores, and reveals once. PostStart signs
  in as the service account and creates or re-points this account through
  `POST /api/auth/users` and `PATCH /api/auth/users/<username>`, which is the
  only path that can set a password after first boot.

Both are `admin`. The app's non-admin role carries a feed allowance, and an
instance whose feeds are the product should not be silently capped by the
account Bloud created on the operator's behalf.

The username is fixed as `bloud` because the reveal snippet is a URL and a
password; it has no field for a username, so the name has to be the half that
never changes.

### What the split cannot follow

If someone changes the `bloud-service` password inside the app, Bloud can no
longer sign in, and there is no API that sets a password without an
authenticated admin. PostStart logs that plainly and returns without failing the
node: the app is up and its feeds work, and parking the node in ERROR would take
them down over a condition only the operator can undo. The recovery is to set the
service account's password back to what the host holds, which means reading it
out of `secrets.json`.

## Config delivery: a host-only env file

The app takes its configuration from process environment and has no API for its
boot-time settings, so PreStart writes `{{appDataDir}}/config/env` and
`metadata.yaml` declares it as the container's `envFile`. The runtime reads that
file on the host when it creates the container; it is never mounted, so the
passwords in it stay out of the container spec and out of `podman inspect`.

`environment:` could not carry these values: it renders static metadata, and
three of the four are per-install secrets.

| Variable | Value | Why |
| --- | --- | --- |
| `REQUIRE_AUTH` | `true`, always | see "Why `none` is the honest strategy" |
| `PODLY_ADMIN_USERNAME` | `bloud-service` | the service account |
| `PODLY_ADMIN_PASSWORD` | host-held secret | seeds the service account on first boot |
| `PODLY_SECRET_KEY` | minted once, then kept | signs every session cookie |

With no secret store (the CLI and conformance shapes) the password line is
omitted rather than written empty. The app raises at startup when
`REQUIRE_AUTH=true` and the password is missing, which is the right outcome for
an install whose store is broken, and the file stays byte-identical across
passes, which is what keeps a resync a read-only diff.

### The signing key is the session store

Podly has no server-side session table. A session is a cookie signed under
`PODLY_SECRET_KEY`, and the app never looks at it again after checking that
signature. Left unset, the app generates an ephemeral key at every start, which
silently logs every device out on every container restart.

So Bloud mints one and keeps it, by reading the existing env file back and
reusing the value it finds. That is also why a plain resync must not rotate it:
the periodic pass would then end every session about once a minute.

The same fact gives the revoke control its meaning. `RevokeSessions` rewrites the
env file with a freshly minted key, and the forced recreate installs it. Every
cookie the app ever issued stops verifying, which is the only way to end a session
in an app that keeps no list of them. It is deliberately blunt, and it is what
the control claims.

## What Bloud leaves alone

- **Feed tokens.** The app mints and displays them. Bloud neither reads nor
  proxies them.
- **`PODLY_TRUSTED_PROXY`.** Unset, on purpose. It exists so the app's HTTP
  server accepts
  `X-Forwarded-Proto` from one named proxy peer, and the app's feed URL builder
  (`_get_base_url` in `src/app/feeds.py`) reads that header from the raw request
  regardless, so Traefik's value reaches it either way. The variable also cannot
  fix the thing it looks like it fixes: the app passes
  `trusted_proxy_headers={"x-forwarded-proto"}` only, deliberately keeping
  `REMOTE_ADDR` as the socket peer, so the login rate limiter sees Traefik's
  address for every visitor whether or not it is set.
- **`SESSION_COOKIE_SECURE`.** Hardcoded `False` upstream so plain-http installs
  work. It is not a Bloud setting, and it is one of the things a real TLS story
  has to revisit.

## The image, and why `lite` is the default

The same release publishes two variants. `latest` is 4.73 GB of compressed
layers, one of which is 4.38 GB of Python packages: local Whisper and its CPU
torch. `lite` is 0.45 GB compressed, 1.23 GB unpacked, and has no local model: it
calls an OpenAI-compatible `/v1/audio/transcriptions` endpoint instead.

Bloud ships `lite`. The full variant's size is not a podcast feed's size: it is a
machine-learning runtime, and paying about nine gigabytes per install to cache a
model that a home server then runs on a CPU is the wrong trade for the thing the
app is for. Transcription is also the kind of capability an instance can point at
a shared endpoint, which is what the [`transcription`
contract](../../docs/features/transcription.md) is for: the operator declares one
endpoint in Settings, and every app that transcribes gets it.

The honest cost of the choice: with no endpoint configured, `lite` cannot
transcribe, so it cannot find ads. The contract is `required: false` because that
is a degraded app rather than a broken install, and the app says so in its own
UI. An operator who wants local transcription can pin the `latest` tag in their
own metadata; nothing in Bloud prevents it, and the full image needs none of this
wiring.

## Transcription: the endpoint comes from Settings

The app's config store holds a whisper section, and `PUT /api/config` writes it.
Bloud fills three fields when the instance declares an endpoint:

```json
{"whisper": {"whisper_type": "remote", "base_url": "<endpoint>", "api_key": "<key>", "model": "<id>"}}
```

`whisper_type` has to travel with the rest: the app's writer switches on the
section type before it maps any field, so a payload of bare `base_url` and
`api_key` lands in the local-whisper columns and does nothing.

Two properties of that API shaped the implementation:

- **The key comes back masked.** `GET /api/config` returns `api_key_preview`,
  the first four and last four characters. So Bloud cannot ask whether the app
  already holds the key it is about to send, and gates the write on a digest of
  what it last applied (`{{appDataDir}}/config/whisper.state`) instead. Without
  that gate the periodic resync would rewrite the app's config store once a
  minute forever.
- **The app merges rather than replaces.** Fields Bloud does not name (language,
  timeout, chunk size) keep whatever the operator set, and Bloud names three.

With no transcription endpoint configured Bloud writes nothing at all, so an
operator who filled the app's own Config page before Bloud had an opinion keeps
it. The same rule protects an edit made after: a pass whose digest is unchanged
leaves the app's settings alone even if a human changed them, because Bloud
cannot tell an operator's key from its own and re-delivering on a timer would be
Bloud overwriting a person.

## The ad classifier: the second model endpoint

Transcription is only half of what the lite image needs. The app dials a second
OpenAI-compatible endpoint to decide which spans of the transcript are
advertising, and that is the only part of its job a feed reader cannot already do.
Bloud wires it from the `inference` contract (Settings -> AI), writing:

```json
{"llm": {"llm_model": "openai/<model>", "openai_base_url": "<endpoint>", "llm_api_key": "<key>"}}
```

Two things about that field are not obvious from the app's own UI, and both were
found by running a real episode through it rather than by reading its docs.

**The model name needs a provider prefix.** The app calls `litellm.completion()`,
and litellm routes on the prefix: a bare `qwen3.8-flash-next` raises
`BadRequestError: LLM Provider NOT provided` client-side, before a request is
sent, while `openai/qwen3.8-flash-next` reaches the same endpoint and answers.
That is why upstream's own default is `groq/openai/gpt-oss-120b`. Bloud's
inference contract is OpenAI-compatible by definition, so `openai/` is the correct
prefix for every provider it can hold; a name already carrying a slash is left
alone. Verified against the running image with both spellings against one
endpoint.

**A keyless endpoint still needs a key.** The app builds an OpenAI SDK client for
both halves, and `OpenAI(api_key=None)` refuses to construct at all; an empty
string constructs, but the app's own LLM path turns one into none
(`llm.get("llm_api_key") or ...`). So a provider with no credential is handed the
literal string `placeholder`, which the endpoint ignores: measured against a
keyless upstream, both `/v1/audio/transcriptions` and `/v1/chat/completions`
answer 200 with `Authorization: Bearer placeholder`.

### What a real ingestion looks like

Measured end to end on a native runtime with a real ad-bearing episode (~33 min):

| stage | result |
| --- | --- |
| feed add | 25 episodes ingested |
| transcribe | 1131 segments in 2 chunks, remote whisper |
| identify ads | 22 litellm calls, 103 identifications, 194 s flagged |
| refine boundaries | per-ad pass, ~5 min |
| output | 30.4 min ad-free mp3, 30,139,479 bytes |
| serve | ad-free RSS enclosure reports exactly that byte count |

The classification pass is the slow part and it is sequential: 90-segment windows
(60 new plus 30 overlap) at one call each, then a refinement call per ad block.
It is I/O wait on the upstream, not load on the container.

## Two processes in one container

The container runs the writer service (which owns every database write,
including user mutations, and is reached over an IPC socket on
`127.0.0.1:50001`) and the web app. The image's start script waits up to
30s for the writer before starting the app, so both are in one container by
upstream design and there is nothing for the graph to order.

`PUID` and `PGID` are set to the upstream compose defaults. The image's
entrypoint only drops privileges when both are present; with neither it runs the
whole app as root.

## Why Bloud sets `SERVER_THREADS`

`src/main.py` reads `SERVER_THREADS` and falls back to **one** worker thread when
it is unset, and every upstream compose file ships `SERVER_THREADS=1`. A feed add
is a synchronous `feedparser.parse(url)` inside the request handler
(`src/app/feeds.py`) with no timeout, so one feed host that accepts the TCP
connection and then sends nothing holds the only worker, and every later request
queues behind it indefinitely. Bloud's own health probe queues with them.

Observed on a real install: adding a feed from `www.cbc.ca` left the app serving
nothing for an hour. The last line it logged was the fetch, zero requests were
served after it, and the container held one ESTABLISHED, silent socket to the
only address that host resolves to. `waitress.queue` depth climbed the whole time.
That host answers `curl` in 1.6s and stalls Python user agents: the same fetch
times out at 20s from the host and at 15s from inside a container.

Measured A/B against `2.7.5-lite`, same feed both times:

| container | app still answers |
| --- | --- |
| `SERVER_THREADS` unset (1 worker) | no, every request times out |
| `SERVER_THREADS=4` | yes, 200 in 1.5ms, and `/api/config` in 22ms |

A well-behaved feed (NPR's RSS) adds in 5.6s on either setting, so this is about
one hostile host, not about feed adds being slow.

Four is `waitress`'s own documented default. It is safe here rather than merely
smaller: the app opens SQLite in WAL mode with a 90s `busy_timeout`
(`src/app/__init__.py`), which is the configuration built for concurrent
connections, so concurrent workers wait instead of erroring.

This shrinks the blast radius from one stalled feed to four. It does not close the
hole: the fetch has no timeout upstream, so four hostile feeds still wedge the
app. The real fix is a `timeout=` on the upstream `feedparser.parse` call.

## Icon

The selfh.st icon set has no Podly entry (`png/podly.png` and
`png/podly-pure-podcasts.png` both 404), so the app keeps the upstream logo:
`src/app/static/images/logos/manifest-icon-512.maskable.png` from the
`podly_pure_podcasts` repository at tag `2.7.5`, committed byte-for-byte with no
resize or recompression.

## Testing

Against a fake that models the app's routes one for one
(`apps/podly/fakepodly_test.go`):

- `apps/podly/configurator_test.go`: the env file's contents and mode, the
  read-only steady state, the no-secret-store shape, and that a revoke rotates
  only the signing key and leaves PreStart converged afterwards.
- `apps/podly/accounts_test.go`: account creation, rotation reaching the app, a
  deleted account coming back, and the two cases where the pass must decline to
  write (auth off, service password rejected inside the app).
- `apps/podly/transcription_test.go`: the endpoint reaching the app's config
  store, no write when nothing is declared, no rewrite on a steady-state pass,
  a changed endpoint or a rotated key reaching the app, an operator's own edit
  surviving the pass after it, and untouched fields staying untouched.
- `apps/conformance_test.go`: the shared harness, including that PreStart is
  offline and idempotent.

### How to verify

The hand-run checks above covered the app's behaviour. The integration tier is
the gate for the orchestrator's path, and on the lite image it is an ordinary
pull:

```bash
./bloud validate --tier integration --test-run podly
```

What to look for, in order:

1. The container reaches `RUNNING`, which means the health check passed and so
   the app booted with `REQUIRE_AUTH=true` and a password it accepted.
2. The dashboard's reveal-once control shows a password, and that password signs
   in at `http://podly.<host>:8080`.
3. Rotate from the dashboard, then sign in with the new value. This is the claim
   `rotate: bloud` makes and the one an environment-only delivery could not keep.
4. Revoke sessions, and confirm a browser that was signed in is signed out.
5. Set Settings → Transcription, then confirm the app's own Config page shows the
   endpoint and model, and that a second pass does not rewrite them.
6. Subscribe to a feed from the app's own UI in a podcast player, and confirm the
   enclosure URLs carry the deployment's scheme.

## Validation strategy

`sso.strategy` is `none`: the only credential paths are the app's own session
cookie and the per-feed tokens a player presents. The credential a person types
is a Bloud-minted `clientPassword`, so the browser journey is a password login
rather than an SSO round trip.
