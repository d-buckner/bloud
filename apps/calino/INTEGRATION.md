# Calino Integration

## Status: Working

[Calino](https://calino.io) is a browser-based CalDAV calendar: a React SPA
behind Caddy with no backend of its own. Bloud installs it as one container,
puts it behind the identity provider, and requires a CalDAV server to be
present, because a calendar client with nothing to read is an empty window.

- Image: `ghcr.io/ivan-malinovski/calino:0.36.0` (pinned)
- SSO strategy: `forward-auth`
- Required integration: `caldav` → Radicale
- Public URL: `http://calino.localhost:8080` (app subdomain on the Bloud
  base domain; `localhost:8180` direct for debugging)
- Storage: none on the server. Calino keeps its data, and the CalDAV
  credentials it was given, in the visitor's browser.

## Why the app requires a CalDAV server

Calino is a client and nothing else. It serves no calendar, owns no data, and
has no account system: the first thing it asks for is a CalDAV server URL.
Installed on a Bloud instance with no DAV server, it converges cleanly and
then shows a setup form that leads nowhere.

So the pairing is declared rather than assumed:

```yaml
integrations:
  caldav:
    required: true
    multi: false
    compatible:
      - app: radicale
        default: true
```

`required: true` is what makes the orchestrator record Radicale as the
provider before Calino, install it if it is missing, and refuse to remove it
while Calino is installed (`PlanRemove` blocks with "calino requires a
caldav"). The graph edge is the same fact the binding is built from, so the
install order, the removal guard, and the dashboard's dependency view cannot
disagree about it.

## Why the contract carries no credential

The `caldav` contract publishes no secret, and that is the design rather than
an omission. A DAV server authenticates the *person*: the password they type
into their calendar client, which Radicale verifies against the Bloud identity
provider over LDAP. There is no machine credential in that exchange, and a
contract that could carry one would be a contract for shipping a user's
password into a second app's configuration file, where it would sit on disk
and get read by everything that app runs.

What the consumer gets instead is the address, in both vantages:

| Field | Value | Who can use it |
|---|---|---|
| `BaseURL` | `http://apps-radicale:5232` | a container on `apps-net` |
| `PublicURL` | `http://radicale.localhost:8080` | a browser |
| `Path` | `/` | appended to whichever one was chosen |

`PublicURL` is the half a browser-based consumer cannot derive for itself, and
the container name does not resolve there at all, so the resolver composes it
from the instance's live public address. The configurator logs the composed
address on every pass, because it is the one thing the operator ends up having
to tell a user and nothing else in the system ever writes it down.

## Why the strategy is `forward-auth`

Calino has no login of its own to replace, so the question is not how to
federate its authentication but whether the page should be loadable at all.
It should not: the app stores the user's CalDAV password in browser storage,
so an origin anyone on the LAN can open is a credential store behind no door.
Forward-auth at the ingress puts the identity provider in front of every
request for the bundle.

The DAV traffic itself does not traverse that gate. Once the page is loaded,
the browser talks to `radicale.<host>` directly, under its own Basic
credential, and Radicale checks it against LDAP. That is why Calino's strategy
(`forward-auth`) and Radicale's (`ldap`) are different and both correct: they
gate different requests.

## The generated configuration: there isn't one

`PreStart` is a documented no-op. Calino reads no environment variables and
mounts no state; its whole configuration lives in the person's browser and is
set through the app's own settings UI. There is no file for the configurator
to converge, so it asks for no restart and changes nothing, on every pass.

`PostStart` does the one thing metadata cannot assert: it fetches the SPA
shell and checks that the body carries `id="root"`, the mount point the bundle
renders into. A Caddy error page, a wrong document root, or a bundle that
never finished copying all leave a container that reports RUNNING while
showing the user nothing; the mount check is what tells them apart. The
`#root:not(:empty)` assertion in the browser spec is the same idea one layer
up: the shell ships with the div empty, so it is only non-empty once the
bundle actually ran.

## The port

The upstream image serves from Caddy on 8080 with the Caddyfile baked into
the image, and 8080 is Traefik's own entrypoint on a Bloud host. Rather than
fork the image to move the container's listen address, the host side carries
the Bloud port and the container keeps its own:

```yaml
port: 8180
ports:
  - host: 8180
    container: 8080
```

Traefik dials `localhost:8180`, the health check runs inside the container
against 8080, and the configurator probes the host side. The trade-off is
that `port` is the published port rather than the container's own; nothing
consumes Calino's container address, because Calino provides no contract.

## What the user does

Bloud cannot preconfigure the account for them. Calino bakes its
preconfigured-account file into the JS bundle at build time, which would mean
building a custom image per install and sharing one master password with every
user; the app's own settings UI is the right place for this.

1. Open Calino from the Bloud home screen and sign in through the identity
   provider.
2. Add a CalDAV account: the Radicale address (`http://radicale.<host>`,
   logged on every reconciliation pass), with their own Bloud username and
   password.
3. The calendars, contacts, and tasks Radicale holds for that account appear.

## What is not wired

- **Cross-origin reach from the browser to Radicale.** Calino dials
  `radicale.<host>` from `calino.<host>`, which is cross-origin, and
  Radicale sends no CORS headers. The upstream answer is a CORS proxy
  (`ghcr.io/ivan-malinovski/calino-proxy`) or headers added at the reverse
  proxy; neither is wired here yet. The app installs, loads, and authenticates;
  the DAV round trip from the browser is what a follow-up has to settle.
- **Preconfigured accounts.** Build-time only upstream, so per-install by
  definition. See [What the user does](#what-the-user-does).
- **Server-side state of any kind.** Nothing to back up, nothing to migrate,
  and a browser profile is where the app's data lives.

## Verification

- `apps/calino/configurator_test.go`: the shell probe accepts the bundle and
  rejects an error page, a non-bundle 200, and tolerates an unreachable
  container; the paired provider is reported with its public address.
- `services/host-agent/internal/engine/orchestrator/integration_bindings_test.go`:
  the `caldav` binding carries both addresses and no credential, and follows
  the live host set.
- `services/host-agent/internal/catalog/provides_test.go`: the provider-side
  declaration is validated against the contract, including rejecting a
  published secret.
- `e2e/tests/calino.spec.ts`: installing Calino brings Radicale with it, the
  bundle is gated by forward-auth, and after sign-in the SPA mounts.
