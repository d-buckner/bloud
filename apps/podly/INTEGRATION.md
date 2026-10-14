# Podly Integration

## Status: implemented, not exercised against the real image

`podly` is [Podly Pure Podcasts](https://github.com/podly-pure-podcasts/podly_pure_podcasts):
it downloads a podcast episode, transcribes it, finds the ad segments, silences
them, and republishes the result as an RSS feed a podcast player can subscribe
to. It is the first catalog app whose job is to rewrite media rather than serve
it.

- Image: `ghcr.io/podly-pure-podcasts/podly-pure-podcasts:2.7.5-latest` (pinned)
- SSO strategy: `none`. The human credential is a `clientPassword`
  (`reveal: once`, `rotate: bloud`)
- Port: `5001`, published to the host and routed by Traefik
- State: `{{appDataDir}}/instance` mounted at `/app/src/instance`
- Config: `{{appDataDir}}/config/env`, declared as the container's `envFile`

The image has never been run on the machine this integration was written on. The
`latest` variant is 4.73 GB of compressed layers (read from the manifest of
`2.7.5-latest`, not pulled), and the checkout's disk could not hold it. What is
verified is the catalog contract, the configurator against a fake that models
the app's auth routes route by route, and the shared conformance harness. The
install and reconcile journey is the open item, and the integration tier is where
it closes; see "How to verify" below.

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

- **Transcription providers.** Podly can use local Whisper (bundled in the
  `latest` image variant) or a hosted provider, configured in its own Config
  page. The API keys for the hosted options (`GROQ_API_KEY`, `LLM_API_KEY`, and
  friends) are operator choices with billing attached; Bloud does not set them.
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

## The image, and the `lite` variant

The pinned tag is `2.7.5-latest`. The same release publishes `2.7.5-lite`, which
is 0.45 GB compressed instead of 4.73 GB, and drops local Whisper: transcription
then requires a hosted provider. The full variant is the default because it is
the one that works with no third-party account, which is the point of a home
server. `estimatedSizeMB` in the metadata reflects the full variant.

The container runs two processes: the writer service (which owns every database
write, including user mutations, and is reached over an IPC socket on
`127.0.0.1:50001`) and the web app. The image's start script waits up to
30s for the writer before starting the app, so both are in one container by
upstream design and there is nothing for the graph to order.

`PUID` and `PGID` are set to the upstream compose defaults. The image's
entrypoint only drops privileges when both are present; with neither it runs the
whole app as root.

## Icon

The selfh.st icon set has no Podly entry (`png/podly.png` and
`png/podly-pure-podcasts.png` both 404), so the app keeps the upstream logo:
`src/app/static/images/logos/manifest-icon-512.maskable.png` from the
`podly_pure_podcasts` repository at tag `2.7.5`, committed byte-for-byte with no
resize or recompression.

## Testing

Covered without the image:

- `apps/podly/configurator_test.go`: the env file's contents and mode, the
  read-only steady state, the no-secret-store shape, and that a revoke rotates
  only the signing key and leaves PreStart converged afterwards.
- `apps/podly/accounts_test.go`: account creation, rotation reaching the app, a
  deleted account coming back, and the two cases where the pass must decline to
  write (auth off, service password rejected inside the app).
- `apps/conformance_test.go`: the shared harness, including that PreStart is
  offline and idempotent.

### How to verify

The integration tier is the gate that has never run for this app, and it needs
the disk to pull the image:

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
5. Subscribe to a feed from the app's own UI in a podcast player, and confirm the
   enclosure URLs carry the deployment's scheme.
