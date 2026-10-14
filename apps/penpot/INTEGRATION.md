# Penpot Integration

## Status: implemented, verified against the image's own entrypoint and source; booted once in CI, which is where the MCP probe was caught

`penpot` is [Penpot](https://penpot.app): an open-source design platform for
interfaces, design systems, and prototypes. It is the first catalog app that
ships its own MCP server, and that is also its whole AI story: Penpot has no
bring-your-own-key inference endpoint and no model of its own. What it exposes is
a tool surface, and the model that drives it is whatever the harness on the other
end is using. In a Bloud install that harness is Hermes, so Penpot's AI arrives
through the `mcp` contract rather than through `inference`.

- Images: `penpotapp/{frontend,backend,exporter,mcp,admin-console}:2.18.3`, plus
  `postgres:15` and `valkey/valkey:8.1` (all pinned; the five Penpot images are
  1,576 MB compressed between them, which is why `estimatedSizeMB` is 1850)
- SSO strategy: `native-oidc`, callback `/api/auth/oidc/callback`
- Port: `9002` (the frontend's nginx), published and routed by Traefik
- Containers: 7, the same topology as Penpot's own
  [`docker-compose.yaml`](https://github.com/penpot/penpot/blob/2.18.3/docker/images/docker-compose.yaml)
  (frontend, backend, exporter, mcp, admin-console, postgres, valkey)
- Config: `{{appDataDir}}/config/penpot.env`, declared as the `envFile` of every
  container that needs it
- Integration: `mcp`, provided, published only once the operator enables MCP
  inside Penpot

Everything below marked as read-from-source was checked against the pinned
`2.18.3` tag on 2026-10-14 (the frontend image's entrypoint and nginx templates,
the backend config and SSRF modules, the Integrations page, the compose
topology), and against the published tags on Docker Hub. What has *not* happened
yet is running the stack: the install through the API, the graph, and the OIDC
round trip are the integration tier's `penpot` leg and the
`e2e/tests/penpot.spec.ts` journey, and both are new with this app.

## The configuration is one file, and the file is the only channel that works

Penpot's environment is large and per-install: the OIDC client credential Bloud
registers, a signing key that must survive restarts, seven container addresses.
`metadata.yaml` `environment:` cannot hold any of it, so the configurator renders
`penpot.env` and every container that needs it declares it as its `envFile`.

Two nodes write that file, and that is deliberate rather than an oversight. The
backend node writes it because it is first in the graph. The frontend node writes
it too because a declared `envFile` that does not exist is a container-create
error, and the frontend must not be able to reach `EnsureContainer` before the
file does. The renderer is deterministic and the one value that cannot be
recomputed is read back out of the previous file, so the second writer converges
to `changed=false` and no container is recreated.

`PENPOT_SECRET_KEY` is that value. It signs Penpot's sessions and the tokens
Penpot mints, so regenerating it on a resync would log everyone out on every
reconciliation. It is generated once and then read back from the file, which is
also where every container that has to agree about it already reads it from. It
is not published through a contract because nothing outside the install needs it.

The database credentials are the exception: `{{postgresPassword}}` is rendered by
the catalog, so those three keys live in `metadata.yaml` `environment:` and the
env file deliberately does not mention them.

## The flags are load-bearing in a way that is easy to miss

`PENPOT_FLAGS` is not a feature toggle list. The frontend image's
[`nginx-entrypoint.sh`](https://github.com/penpot/penpot/blob/2.18.3/docker/images/files/nginx-entrypoint.sh)
greps that exact string to decide which nginx location blocks exist:

```bash
if [[ $PENPOT_FLAGS == *"enable-mcp"* ]]; then
    export PENPOT_MCP_URI=${PENPOT_MCP_URI:-http://penpot-mcp:4401}
    ...
else
    rm -f /etc/nginx/overrides/server.d/mcp-locations.conf
fi
```

So with `enable-mcp` absent the MCP container is running, healthy, and reachable
by nothing: the proxy rule that would route to it was deleted at container start.
`enable-admin-console` works the same way. That is why `PostStart` reads
`/js/config.js` back over HTTP and fails the node if the flags it serves are not
the flags the install was configured with: it is the only check that can tell
"the file was written" from "the container booted from the file".

The same entrypoint is what makes the other two assertions in that file honest.
`PENPOT_PUBLIC_URI` is appended to `config.js` verbatim as
`var penpotPublicURI = "...";`, and `PENPOT_OIDC_NAME` is substituted into
`var penpotOIDCName = "...";`, which is the string the login page uses as the
provider button's label. The e2e spec clicks a button named **Bloud SSO**, so the
browser proves the OIDC block reached the SPA.

The flags Bloud sets, and why:

| Flag | Why |
| --- | --- |
| `disable-registration` | closes the password form; the identity provider is the only way in |
| `enable-oidc-registration` | lets a first SSO sign-in create the Penpot profile from the provider's claims. Penpot checks the two registration flags independently, so closing the password form does not close the SSO one |
| `disable-email-verification` | stands in for an SMTP server Bloud does not run; the provider already asserted the address is real |
| `enable-login-with-oidc` | shows the provider button |
| `enable-mcp` | installs the nginx rules for `/mcp/stream`, `/mcp/ws`, `/mcp/sse`, and shows the MCP section on the Integrations page |
| `enable-admin-console` | installs the `/admin-console` rules |

`disable-secure-session-cookies` is deliberately not in the list. Penpot's own
config adds it whenever the public URI is plain http on a non-localhost host
([`backend/src/app/config.clj`](https://github.com/penpot/penpot/blob/2.18.3/backend/src/app/config.clj),
`parse-flags`), which is exactly Bloud's default shape, and a second copy of that
rule would be a second place to be wrong.

## The OIDC provider can be configured and unreachable at the same time

Two failure modes here are invisible from the dashboard, and both were found by
reading the code rather than by running it.

**Discovery is server-side and it is SSRF-checked.** Penpot fetches the issuer's
`/.well-known/openid-configuration`, the JWKS, the token, and the userinfo
endpoints from inside the backend container, through its own HTTP client, and
`app.util.ssrf/check` refuses any target that resolves to a private, loopback,
link-local, or carrier-grade-NAT address unless the hostname appears in
`:ssrf-allowed-hosts` (case-insensitive exact match on the host, no port).
Bloud's issuer on a localhost install is `http://sso.localhost:8080`, which
resolves to loopback by design. Without the allowlist entry the provider is
configured, the login page shows the button, and every sign-in fails.

`PENPOT_SSRF_ALLOWED_HOSTS` is set to exactly the issuer's hostname, which keeps
the bypass as narrow as the dependency: every other address Penpot may be asked
to fetch is still checked. The global OIDC provider has no `skip-ssrf-check?`
knob (`get-oidc-config` in `app.auth.oidc` does not read one), so the allowlist is
the only lever, not a shortcut past a bigger one.

**A provider that fails to initialize does not fail the boot.** The provider is
built once, at startup, and the whole initialization is wrapped:

```clojure
(catch Throwable cause
  (l/warn :hint "unable to initialize auth provider" :provider "oidc" :cause cause))
```

The container comes up healthy, the node reaches `RUNNING`, and the app has no
identity provider. This is why the integration leg greps `podman logs
apps-penpot-backend` for the two strings Penpot writes (`provider initialized`
and `unable to initialize auth provider`): it is the only signal that
distinguishes a working SSO from a healthy-looking broken one, and no unit test
can make it.

The rest of the OIDC block follows Penpot's requirements rather than Bloud's
preferences. `email` must be in the scopes because Penpot identifies the account
by email claim and refuses to link a sign-in whose address it cannot see; Bloud's
native-oidc mapping already asserts `email_verified`, which is what Penpot
requires to link or create a profile. `PENPOT_OIDC_BASE_URI` takes the issuer,
not the origin, because Penpot appends the discovery path itself.

## MCP: Bloud reads the key, it does not make one

Penpot's MCP is three pieces, and only one of them is a container
([Penpot's MCP documentation](https://help.penpot.app/mcp/), retrieved
2026-10-14):

- the **MCP server** (`apps-penpot-mcp`), which exposes tools to an AI client;
- the **MCP plugin**, which runs *inside a Penpot browser tab* and is what
  actually touches the file;
- the **MCP client**, which connects to the server with a URL and an MCP key.

The consequences of that shape are what the configurator has to respect:

1. **The key is Penpot's.** A person goes to *Your account → Integrations → MCP
   Server*, enables it, and Penpot mints one key per user and shows it once. The
   same screen hands out the server URL with the key embedded as `userToken`.
   Bloud cannot mint this value: a key Bloud invented would authenticate against
   nothing and would not be the string the operator's browser tab dials with. So
   `mcp.go` reads the key out of Penpot's own database, scoped to the operator's
   email and to `access_token` rows of type `mcp` that have not expired.
2. **Nothing to publish is a real state, not a failure.** Before anyone enables
   MCP in Penpot there is no key, and Bloud publishes no `httpToken` and no
   `path`. A consumer writes no tool namespace, which is the correct rendering of
   "nobody has turned this on yet". Both are cleared together: a path with an
   empty token would be a half-published offer.
3. **Rotation is an outside-world change with no intent behind it.** Regenerating
   the key in Penpot's UI revokes the old one immediately. Nothing tells Bloud.
   That is what the periodic reconciliation pass is for: `PostStart` re-runs on
   every resync, re-reads the key, and republishes. Every failure path in the sync
   logs and returns nil, because a database that is briefly unreachable must not
   take a working design tool out of the dashboard to report a missing tool
   namespace.
4. **The endpoint is a path with a query string.** The frontend's nginx proxies
   `/mcp/stream` to `$PENPOT_MCP_URI/mcp$is_args$args`, and the credential
   travels as `?userToken=<key>`. So the published `path` is
   `/mcp/stream?userToken=<key>`, and `httpToken` carries the same string because
   the `mcp` contract's consumer sends a bearer header and Penpot reads a query
   parameter; the harness sends both and Penpot uses the one it understands.
5. **A tool call needs a live browser tab.** The plugin runs in the page. If the
   browser freezes or unloads the tab, MCP fails fast and says the plugin is not
   connected. Bloud cannot fix that and does not pretend to: the operator's
   Penpot tab has to be open for the tools to do anything. This is a property of
   Penpot, not a Bloud limitation, and it is worth saying out loud because every
   other MCP provider in the catalog answers without a human present.

The MCP container publishes no port of its own. It is reachable only through the
routed Penpot origin, behind the user's key, which is also how Penpot's own
compose file runs it.

## Two things that are quiet if you get them wrong

`PENPOT_DISABLE_IPV6_LISTEN=true`. The frontend image's nginx defaults to
`listen [::]:8080 default_server ...`, and the entrypoint clears that directive
when this variable is truthy. On a podman network with no IPv6 subnet the bind
fails, and the failure is an nginx that never starts rather than a degraded
feature.

`PENPOT_BACKEND_URI`, `PENPOT_EXPORTER_URI`, `PENPOT_MCP_URI`,
`PENPOT_MCP_URI_WS`, `PENPOT_ADMIN_CONSOLE_URI`, `PENPOT_INTERNAL_URI`,
`PENPOT_REDIS_URI`. These are written in `env.go` rather than in
`metadata.yaml` because the images fall back to Penpot's compose service names
(`penpot-backend`, `penpot-mcp`, ...) when they are unset, and Bloud's containers
are named `apps-penpot-*`. A missing one is a container that starts and points at
a host that does not exist. The values are the image defaults with the names
replaced; the ports (6060 backend, 6061 exporter, 4401 MCP stream, 4402 MCP
websocket, 3000 admin console, 8080 frontend) come from the same entrypoint
defaults.

## The admin console is a dependency of every page, not an optional extra

`enable-admin-console` is not a menu item that can be missing harmlessly. With the
flag on, `get-profile` (the RPC the SPA issues first, and `::rpc/auth false`, so
anonymous visitors hit it) ends in `(with-nitrate-licence profile cfg)`, a call
from the backend to the service at `PENPOT_ADMIN_CONSOLE_URI`. When that call
fails, `backend/src/app/http/errors.clj` answers
`503 {:type :nitrate-unavailable}` and the frontend renders "Penpot is
temporarily unavailable due to a system issue" in place of the login page.

So an install where every container reports healthy can have no way to sign in.
That is what the first CI boot actually hit: the frontend's nginx was serving,
`/js/config.js` was exactly right, and the browser got the unavailable page.

The cause was the shared env file, and it is worth keeping the reasoning down
because the two readings of one variable are both correct. Penpot's compose
passes `PENPOT_ADMIN_CONSOLE_URI` to the backend and the frontend, where it means
"the address to dial the console on", and pointedly does not pass it to the
console itself. The console reads the same name as "the path I am served under",
and its bundle refuses any other answer because its asset URLs are baked with
that path at build time. Run the image with the shared file's value and it
applies its database migrations, then exits 1:

```text
Error: Admin Console must be served under "/admin-console/", but
PENPOT_ADMIN_CONSOLE_URI puts it at "/". The client bundle is built with that
path baked into its asset URLs, so it cannot be moved at runtime
```

Bloud therefore writes two dotenv files. `penpot.env` is the wide one the
backend, frontend, and exporter share; `penpot-admin-console.env` is the narrow
one (public URI, signing key, internal URI: the same set the compose passes to
that service), and `adminEnvKeys` in `env.go` is the list. Both are cut from one
rendered settings slice, because the console derives the shared secret the
backend authenticates to it with out of `PENPOT_SECRET_KEY` (its bundle: `PENPOT_
ADMIN_CONSOLE_SHARED_KEY ?? hkdf(PENPOT_SECRET_KEY, "admin-console")`) and the
two files cannot be allowed to disagree about it.

The routed node's `PostStart` therefore polls `GET /api/main/methods/get-profile`
until it answers 200. It is the only outside-visible check that reaches nginx,
the JVM, PostgreSQL, and the admin console at once, and it is the request the
browser is about to make, so the node stops being "running" in the same terms the
user will experience. A 503 body is kept as the failure text: the first CI boot
after this gate landed said `503 {:type :nitrate-unavailable}` in the install
error, which is what pointed at the console rather than at nginx.

## Health checks

The three Penpot-authored images are Docker Hardened Images, and the `-dev`
suffix on the base tag is the difference between a probe that can run and one
that cannot:

| Image | Base | `/bin/sh`? |
| --- | --- | --- |
| `penpotapp/backend` | `dhi.io/debian-base:trixie-debian13-dev` | yes; its own `CMD` is `["/bin/bash", "run.sh"]` |
| `penpotapp/frontend` | `dhi.io/nginx:1.31.1-debian13-dev` | yes, plus bash; no curl, no wget |
| `penpotapp/mcp` | `dhi.io/node:24.20.0-debian13` | **no**, and no coreutils either |

So the backend and frontend probes are TCP connects through bash's `/dev/tcp`
pseudo-file. The backend uses a TCP connect rather than an HTTP path because the
JVM opens its listener only after migrations and startup tasks, and "something is
listening on 6060" is the readiness signal the image can be checked against
without depending on a management path that is not part of its documented
surface.

The MCP image takes none of that. With no shell, a `CMD-SHELL` probe fails before
`node` is reached: `crun: executable file /bin/sh not found`. Verified by running
the image and exec'ing both forms. The probe is therefore `CMD` (exec'd directly,
no shell in the path) and it opens the port itself instead of speaking the
protocol, because a bare `GET /mcp` is a session request: the server logs it and
allocates a transport for it, every ten seconds, forever.

The stronger checks are in `PostStart`, which is where the config round trip is
verified. The health check only has to say "the listener is up"; the difference
between a container that booted from Bloud's file and one that booted from its
defaults is not visible in a TCP connect.

## What is covered where

| Layer | What it proves |
| --- | --- |
| `configurator_test.go` | the renderer's output: flags, URIs, the OIDC block, the SSRF allowlist, secret-key read-back, and a metadata-agrees-with-code table |
| `apps/conformance_test.go` | both nodes are registered and satisfy `NodeLifecycle` |
| `catalog/resolution_test.go` | Penpot is a `mcp` provider the resolver can wire to Hermes |
| `internal/e2e/penpot_test.go` (integration tier) | install through the real API; `config.js` served on the direct and routed origins; the backend actually reached its issuer; the env file's mode and contents; the MCP contract stays unpublished on a fresh install; uninstall cleans up 7 containers, data, routes, and Authentik |
| `e2e/tests/penpot.spec.ts` | the browser journey: the login page offers **Bloud SSO**, the round trip through Authentik lands on the Penpot dashboard |

## Open questions

- The admin console is optional in 2.18 and Penpot documents it as becoming
  required, so Bloud installs it now rather than making every existing install
  take a topology change later. If it stays optional for a year, it is 93 MB of
  image and one container paying rent for a future requirement.
- The MCP key read reaches into Penpot's schema (`access_token.type = 'mcp'`).
  Penpot does not document that table as an interface, so an upgrade could move
  it. The failure mode is benign: the query returns nothing, the contract
  unpublishes, and the operator's MCP tools disappear from the harness until the
  read is fixed.
- Penpot's own `docker-compose.yaml` ships an optional `mailcatch`. Bloud runs no
  SMTP at all, which is what `disable-email-verification` covers. If a future
  Penpot feature needs outbound mail, that is a new decision, not a flag.

## Validation strategy

`sso.strategy` is `native-oidc`, so the browser journey is the gate that proves
the generated OIDC block reached the running app: a sign-in is only possible if
the container booted from the env file Bloud wrote, and no unit test can reach
that fact. The MCP half has no browser journey to gate (the key is read from
Penpot's own database and handed to a harness), so it is covered by the
configurator tests and the shipped-catalog contract tests instead.
