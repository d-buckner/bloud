# openGym Integration

Status: implemented, not yet run against a live install. Every behavioural claim below
comes from a real podman run of both pinned images on a rootless host, recorded under
"Verified constants". Upstream: <https://github.com/DuarteSantos8/openGym>, MIT,
upstream issue #101 asked for a Bloud recipe.

## What this app is

openGym is a gym and body-weight tracker. A profile is a passkey, workouts are logged
against a per-user exercise library, and the phone app pairs with a web profile by QR
code. It is two containers: a `node:http` API over a JSON file store, and an nginx front
end that serves the built React app and proxies `/api` to the API on one origin.

Bloud runs both, wires the front end to Traefik, and tells the API which origin it is
supposed to recognise. Nothing else about this install needs a configurator.

## Why `none` is the honest strategy

openGym authenticates its own users and has no way to accept an identity from a proxy:
no OIDC client, no LDAP, no trusted-header mode. A profile is a passkey, or a name and a
password where Bloud turns that on, and the session is a cookie the app signs with a key
in its own data directory.

`forward-auth` is worse than useless in front of it, and upstream's self-hosting guide
says so. Three reasons, all of them silent failures:

- The phone app talks only to `/api/`, authenticates with a bearer token, and sends no
  cookie. A gate in front of `/api/` answers the pairing request with a login redirect,
  and pairing fails while both containers log nothing.
- WebAuthn needs the app and its API on one origin, which is why the web image proxies
  `/api` itself. A gate breaks the property the layout exists to keep.
- The app reads the visitor's address from `X-Forwarded-For` to throttle failed
  sign-ins. Behind a second proxy that header is the first proxy's address unless the
  first proxy overwrites it, so the throttle counts the wrong client.

So Bloud configures the app instead of replacing its authentication. It also mints no
credential for it: a profile is created inside openGym, by a passkey ceremony or by the
password form, so there is nothing for the dashboard to reveal and no `clientPassword`
contract to declare.

## The address is part of the app's security configuration

This is the reason the app has a configurator at all.

A passkey is bound to the exact hostname the browser is on. openGym takes that from
`RP_ID`, and separately compares each same-origin request against `ORIGIN`, because on
plain http there is no `Sec-Fetch-Site` to check. Both are derived from the instance's
public URL on every reconciliation and written to a host-only env file the API container
is created from:

| Key | Value for `https://gym.example.com` | Why it is that shape |
| --- | --- | --- |
| `RP_ID` | `opengym.gym.example.com` | A passkey binds to a hostname, never to a port, so no scheme and no port. The app's own subdomain is the name a person is on. |
| `ORIGIN` | `https://opengym.gym.example.com` | Scheme and port included, because this is what the app compares a request's own `Origin` against. |

The file is `{{appDataDir}}/config/env`, declared as the container's `envFile`, so the
runtime reads it on the host at create time and the values never appear in the container
image or in a world-readable path. When the address changes, the file changes,
`PreStart` reports `RestartNeeded`, and the container is recreated, because openGym reads
both values at container create. An address change that did not reach this container does
not look like a broken install: it looks like every sign-in failing "verification
failed", for a reason nobody can see in the app.

## The DNS resolver, which is the part that breaks under podman

The image's nginx config resolves the API name on every request rather than once at
start, so a recreated API container with a new address is picked up without restarting
nginx. That resolution uses nginx's `resolver` directive, and the image's default is
Docker's embedded DNS server at `127.0.0.11`.

Nothing answers at `127.0.0.11` under podman. An unreachable resolver does not fail
fast: nginx waits, every `/api` request hangs until it times out, and both containers
report themselves healthy the whole time. The visible symptom is a UI that loads and
never shows a workout.

podman puts its per-network DNS on that network's gateway, and the gateway address
depends on how many networks already exist on the host, so the number is only knowable
from inside the container. The web container therefore runs a short wrapper instead of
nginx directly: it reads the first `nameserver` from the container's own
`/etc/resolv.conf`, exports it as `RESOLVER`, and then execs the image's own entrypoint,
which substitutes every defined environment variable into the config template. The
`127.0.0.11` fallback is not decoration: an empty `RESOLVER` renders
`resolver  valid=10s`, nginx rejects the config, and the container restart-loops instead
of serving the static app. `apps/ha-mcp` uses the same shape for the same reason.

Measured on a rootless podman host with one network: `resolv.conf` names `10.89.0.1`,
the wrapper exports it, and `GET /api/health` through the published port answers
`{"ok":true,"users":0,"writable":true}`.

## Password sign-in, and what it costs

`PASSWORD_LOGIN=1` adds a name-and-password sign-in next to passkeys. Upstream leaves it
off by default, and the reason to turn it on here is a plain-http deployment reached by
hostname from a phone: WebAuthn needs HTTPS or a `localhost` name, so on that setup the
passkey prompt never appears and the app has no way in at all. It is also the recovery
path for a lost passkey, which upstream documents as the other reason.

The cost is a guessable credential next to an unguessable one, on an app that will often
be reachable over Tailscale rather than only on the LAN. It is off in upstream's default
compose file for a reason. The trade is recorded here rather than buried in a YAML
comment because it is a decision a reader can reverse: set `PASSWORD_LOGIN` to `0` in
`metadata.yaml` and the password form disappears from the login page.

`FIRST_USER_ADMIN=1` makes the first profile created on a fresh install the instance's
admin. On a home server the person who installed it is the person who should be able to
invite someone, disable an account, or hand out a password reset code. Without it that
role belongs to nobody until someone reads the docs and edits `db.json` by hand.

## What Bloud leaves alone

- Accounts, passkeys, invites, and the `admin` flag. All of it is openGym's, edited in
  openGym.
- `INVITE_ONLY`. Left at its default (`0`), so the first profile can be created at all.
  A home server with one or two people does not need an invite code in front of the
  first sign-in. Set it to `1` in `metadata.yaml` once a profile exists to close the
  registration form.
- `ALLOW_GUEST`. Left at its default (`1`): a guest profile needs no account and its
  data is deleted with the browser. It is a demo mode, not a shared account, because
  nothing about it is reachable from another device.
- `SESSION_TTL_DAYS`, push (VAPID), and the media quotas. Defaults are sane and the
  quota is enforced against the same mounted directory.
- No CORS middleware on the route, and none is declared. The API answers
  `Access-Control-Allow-Origin` itself, and a second copy of the header makes the phone
  app reject the response.

## Verified constants

- Images: `ghcr.io/duartesantos8/opengym-api:1.4.1` and
  `ghcr.io/duartesantos8/opengym-web:1.4.1`, the tag of upstream release `v1.4.1`.
  amd64 digests at the time of writing: api
  `sha256:9896afeb463817f3f8bf6d76ccfc99293e1f83e188047427593af54d01418279`, web
  `sha256:5fbfde3965067adef9596db3a443eed892963366a8bee28622ec1910f2be10b4`.
- Unpacked sizes measured with `podman pull` at that tag: api 186 MB, web 297 MB, so
  `estimatedSizeMB: 483`. The web image carries the exercise stills and animations
  (about 80 MB, licensed from Gym visual) that v1.4.0 moved inside the image, so a first
  start downloads nothing.
- The web image runs as uid 101 and its own docs say a rootless runtime needs a port at
  or above 1024, because it cannot bind 80 without
  `net.ipv4.ip_unprivileged_port_start=0`. `NGINX_PORT=8090` matches the app's declared
  port, so the published mapping is 1:1. Upstream's Kubernetes and Helm installs use the
  same shape.
- The API image runs as root, so `{{appDataDir}}/data` needs no ownership fix. It writes
  `db.json`, one `state-<user>.json` per profile, `secret`, `vapid.json`, `audit.log`,
  and `uploads/` into the one mounted directory. Confirmed on a fresh run: `secret` and
  `vapid.json` appear in the mounted host directory, mode 600.
- `GET /api/health` is public and answers `{"ok":true,"users":0,"writable":true}`.
  `GET /api/config` reports `password_login: true`, which is how a test can prove
  `PASSWORD_LOGIN` reached the container without reading a config file.
- `POST /api/register/password` with a name and a password of at least 10 characters
  creates the profile and answers `{"user":{"id":"...","admin":true}}` on a fresh
  install, which is `FIRST_USER_ADMIN=1` observed from outside.
- The API refuses a same-origin check when the request's `Origin` does not equal
  `ORIGIN`. Health and config requests carry no `Origin`, so a host-side probe is not
  affected by the value.

## Icon

`icon.png` is `opengym.png` from [selfh.st/icons](https://selfh.st/icons/), fetched from
the jsDelivr mirror and committed unchanged: 512x512 RGBA PNG, 6.6 KB.

## Testing

- `apps/opengym/configurator_test.go` pins the env file contents, that a moved address
  produces a recreate and a steady-state pass does not, and that the health predicate
  rejects an HTML page where the API should answer.
- `apps/conformance_test.go` runs both nodes through the shared harness: `PreStart`
  offline and idempotent, the web node's reported port against `metadata.yaml`, and the
  declared wait budget inside the framework's phase budget.
- `services/host-agent/internal/e2e/opengym_test.go` installs the app through the real
  API and asserts the behaviour: the health document through the published port, the
  password form being offered, a profile created, and the address file naming the
  instance's own origin.
- `e2e/tests/opengym.spec.ts` is the browser journey.

### How to verify

```bash
./bloud install opengym
curl -s http://localhost:8090/api/health          # {"ok":true,...,"writable":true}
curl -s http://localhost:8090/api/config | grep password_login
cat <dataDir>/apps/opengym/config/env            # RP_ID and ORIGIN name this install
podman exec apps-opengym-web grep -m1 resolver /etc/nginx/conf.d/default.conf
```

The last line is the one to read after any change to the wrapper: it must name an
address, not `${RESOLVER}` and not an empty string.
