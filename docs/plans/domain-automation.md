# Plan: Give the instance a domain and a certificate

> Status: draft

Explores [issue #159](https://github.com/d-buckner/bloud/issues/159), "automatic dns
for domains", and decides between two shapes for it: Traefik doing it inside Bloud, or
Nginx Proxy Manager (NPM) added to the catalog.

## The request

The issue body, in full:

> Right now bloud kind of has to be used with a reverse proxy that has ssl. That means
> manually adding routes for each app. The idea is to have a dns setting in the settings
> menu and do this with traefik.

The issue history carries the decision already. It opened on 2026-10-02 titled "nginx
proxy manager" and labelled `app request`. It was retitled "automatic dns for domains"
on 2026-10-14, and the `app request` label came off on 2026-10-15. This document records
why that retitle is the right call, and what the Traefik shape costs.

## The ask separates into three

The one sentence carries three separable asks.

| Ask | What it means | Status today |
| --- | --- | --- |
| TLS | Bloud serves `https://` with no proxy the operator runs separately | Missing. Nothing listens on 443 |
| DNS | The domain and every app subdomain resolve without a hand edit per app | Partly. The operator creates the records; Bloud names none of them |
| Routing | Nobody adds one route per app, ever | Done inside Bloud. The orchestrator rewrites the Traefik dynamic config after every convergence pass (invariant 7) |

Routing is the ask that is already met, and it is the one that decides the comparison.
Bloud generates a router per app today. The manual per-app route the issue complains
about only appears when a second proxy sits in front and that proxy needs its own host
entry per app. So the real question is: how does Bloud get TLS without creating that
second place?

## Where the tree stands

Shipped, and it is more than it looks:

- The address is one stored origin with a scheme: `GET/PUT /api/settings/public-url`,
  `settings['public_url']`, gated by `ParsePublicURL`
  (`services/host-agent/internal/hostset/hostset.go`). `https://` is accepted. An
  `https://` IP literal is rejected, because no CA issues for a bare address.
- Every generated URL derives from that origin: OAuth redirect URIs, the OIDC issuer,
  the Authentik outpost browser URL, the app launch links. This is the work
  [`tls.md`](tls.md) called "the scheme contract", and it is done.
- `BLOUD_TRUSTED_PROXY_NETS` makes Traefik emit `forwardedHeaders.trustedIPs`, so an
  external terminator's `X-Forwarded-Proto` survives. An empty list selects the
  private-range default. That is the fix for the login stall behind a TLS terminator.
- `HostRegexp` router rules are domain-agnostic
  (`services/host-agent/internal/traefikgen/generator.go`), so app subdomains of any
  domain already route with no config change.
- `apps/vaultwarden/INTEGRATION.md` names the gap exactly, and proves the fix works: an
  unmodified Vaultwarden behind a Traefik TLS entrypoint with `DOMAIN=https://...`
  passes the whole flow.

Missing:

- No `:443` entrypoint, no `certificatesResolvers`, no TLS section at all in the
  generated static config (`services/host-agent/internal/appconfig/traefik.go`,
  `staticConfig`).
- No writable ACME storage. The Traefik container mounts `traefik.yml` and the dynamic
  directory, both read-only (`apps/traefik/metadata.yaml`).
- No DNS provider credential anywhere in Bloud, and no place to put one.
- No UI that tells the operator which DNS records to create.

`docs/operations/tech-debt.md` item 7 already anticipates this: a certificate the CA
returned is named as state genuinely worth persisting, unlike derived credentials.

## Option A: DNS-01 in Traefik, driven from Settings

The operator types a domain and a DNS provider credential once. Traefik gets a wildcard
certificate and serves `:443`. Every app subdomain is HTTPS with no further work,
because the routers already exist.

This is phase 3 of [`tls.md`](tls.md), restated as a product surface.

### The shape

1. Settings gains a Domain panel next to Address. It holds: the DNS provider, the
   provider credential, the ACME registration email, and a staging/production CA switch.
   The domain itself is not a new field. The panel reads the host out of the public URL.
2. Saving submits an intent, per invariant 1. The orchestrator then:
   - renders `websecure` on `:443`, a `certificatesResolvers` block with a DNS-01
     challenge, and an HTTP to HTTPS redirection on `web`;
   - supplies the provider token to the Traefik container as an environment variable,
     never in `traefik.yml`. The static config is written 0644 and is a normal thing to
     attach to a support request;
   - adds a writable ACME storage directory under `BLOUD_DATA_DIR`, so Traefik owns
     renewal and Bloud never parses a certificate file;
   - restarts Traefik, then re-renders the app routers with TLS attached.
3. The panel shows the two DNS records the operator must create, and a check button that
   resolves them and reports what it got. That is a cheap feature and it removes most of
   the support traffic this design will otherwise generate.

### The wildcard detail that decides the router change

Traefik can name a resolver on the entrypoint instead of on every router. That is not
enough here. An entrypoint-level resolver issues one certificate per distinct SNI name,
and it does not produce the wildcard: Traefik only starts a DNS order for a domain a
router declares (traefik/traefik#12109, 2025-09-29).

So the generated routers must declare the domain:

```yaml
tls:
  certResolver: le
  domains:
    - main: "*.example.com"
      sans: ["example.com"]
```

One wildcard order, one certificate, shared by every router with the same SAN set. This
also keeps Bloud clear of the Let's Encrypt limit of 50 certificates per registered
domain every 7 days ([rate limits](https://letsencrypt.org/docs/rate-limits/), page
updated 2026-08-05). A per-subdomain certificate shape spends that budget once per app
per renewal cycle, and an instance with many apps can reach it.

The rule change is small. `traefikgen` already knows the app's catalog id and builds
`HostRegexp`; it gains the domain and emits the TLS block when TLS is on. With TLS off it
emits today's bytes unchanged, which is the regression gate.

### DNS records, and the token blast radius

The operator creates two records: `A` (or `AAAA`) at the apex, and a wildcard `A` for
`*`. Bloud should print both with the resolved address filled in.

A token that can edit a whole zone is a large secret to hand a home server. Recommend
CNAME delegation: the operator points `_acme-challenge.<host>` at a throwaway zone and
gives Bloud a token scoped to that zone alone. A leaked Bloud token then cannot touch the
real zone. Traefik relies on lego, and lego follows the CNAME
([Traefik ACME docs](https://doc.traefik.io/traefik/https/acme/)). See the [scoped ACME
DNS-01 write-up](https://www.bigiron.cc/guides/scoped-acme-dns-01-cname-delegation-and-acme-dns)
for the recipe.

### Why DNS-01 and not HTTP-01

HTTP-01 needs port 80 reachable from the public internet. That contradicts a LAN-only
instance. DNS-01 needs a DNS API token and nothing else: no port forward, no public
address. It is also the only challenge that can issue a wildcard.

### The provider list is the real cost

lego ships more than 200 DNS providers ([lego DNS providers](https://go-acme.github.io/lego/dns/)).
Bloud should not pretend to support all of them.
Each provider needs a live test domain, and an untested provider is a support ticket with
a token in it. Start with a short allowlist, name it in the UI, and fail closed with a
clear message for anything else. A defensible first five: Cloudflare, Hetzner DNS,
Netcup, deSEC, and GoDaddy, plus RFC 2136 for anyone running their own name server.

### Two cases this covers, and one it does not

- **Public domain.** The normal case. Apex and wildcard point at the connection, Traefik
  serves 443.
- **Domain with no public address.** DNS-01 still works, because the challenge is a DNS
  write, not an inbound connection. The records can point at a LAN address and a
  browser on that LAN gets a publicly trusted certificate. This is the case `tls.md`
  phase 3 exists for, and it is the one most home operators are actually in.
- **No domain at all.** Not covered. `bloud.local` and `localhost` can never be HTTPS:
  no public CA issues for a reserved or special-use name. The answer for these operators
  is a domain, or Tailscale Serve, which ships `ts.net` certificates. The tailnet
  integration is out of the product; reviving it is a separate decision.

One platform caveat: the `native` backend runs unprivileged and cannot bind `:80` or
`:443`. DNS-01 TLS on `native` needs non-privileged ports and is a development case only.

### Phase 2 of this option: actually write the records

The literal reading of "automatic dns for domains" is that Bloud creates the records too.
The same provider credential can do it: an `A` at the apex and a wildcard `A`, through
the provider API. It is a second, larger surface, because record semantics differ per
provider and a wrong write edits a live zone. Ship it after the certificate path works,
and gate it behind an explicit action rather than a background reconcile.

## Option B: Nginx Proxy Manager as a catalog app

NPM is a Docker image that wraps OpenResty with a web UI for proxy hosts and Let's
Encrypt certificates. It is not the stagnant project its reputation suggests: v2.16.0
shipped 2026-09-24, the first release since June 2026
([releases](https://github.com/NginxProxyManager/nginx-proxy-manager/releases)). It runs
OpenResty, certbot 5.8, and s6-overlay, stores its state in SQLite in `/data` by default
with MariaDB optional, and serves its admin UI on port 81. It does DNS-01 through certbot
provider plugins and can issue wildcard certificates. It exposes a REST API with
certificate endpoints
([certificates API](https://deepwiki.com/NginxProxyManager/nginx-proxy-manager/9.2-certificates-api)).

### The only topology that does not defeat the point

NPM as the router means one proxy host per app, which is the tedium the issue is about.
So the only coherent NPM design is NPM as a plain terminator: `*.example.com` on NPM,
forwarded to Traefik, with one host entry.

That works. It also makes NPM a container whose entire job is what the next section
describes as a config block.

### What it costs

1. **Port ownership moves.** NPM must hold `:80` and `:443`. Invariant 10 gives `:80` to
   Traefik, and the dev and e2e convention maps guest `:80` to host `:8080`. Installing
   NPM must move Traefik off `:80`, and uninstalling it must move Traefik back. That is
   new orchestrator state and a new class of half-done install.
2. **The hard work is unchanged.** Redirect URIs, the issuer, the Authentik outpost URL,
   and the launch links still have to become `https://`. NPM removes the ACME plumbing
   and nothing else. The ACME plumbing is the smaller half.
3. **Two admin UIs, two accounts.** NPM has no OIDC. It is an `sso: none` app with its
   own local account, so Bloud generates a password for it and shows it once. The
   operator now has a Bloud login and an NPM login, and the NPM one is the one that
   controls whether the site has a certificate.
4. **The DNS token leaves Bloud's secret store.** It lives in NPM's `/data`. The backup
   story, the secret ledger, and any future rotation all have to reach into another
   product's private layout. NPM's own release notes show how that ages: v2.16.0
   removed DNS provider credentials from disk after certbot runs, and the same release
   warns that the certbot bump may break DNS plugins.
5. **Certificate state is invisible to Bloud.** The dashboard cannot show an expiry date
   or warn before a certificate dies. Making it visible means talking to NPM's API with
   a minted credential, which is a new contract, a new payload type, and a new failure
   mode. At that point Bloud has written more code than option A needs.
6. **One more hop, one more log.** Requests become client to NPM to Traefik to app.
   Every support conversation starts by asking which of the two proxies is at fault.
7. **It is a second place where "the address" lives.** Bloud's public URL and NPM's host
   entries can disagree, and the disagreement shows up as a broken login redirect.

### Where NPM genuinely wins

It needs no Bloud code. If the project needed HTTPS next month with no engineering, this
is the shortcut. It also already has the certificate UI that option A has to build: a
list of certificates, their expiry, and a renew button. Option A should copy that screen,
not reinvent it.

## Option C: document the external terminator

The operator already runs NPM, Caddy, nginx, or a Cloudflare Tunnel. Bloud does not
install anything. It needs:

- the public URL set to `https://...`, which works today;
- `BLOUD_TRUSTED_PROXY_NETS` set to the terminator, which works today and already has
  the private-range default;
- one wildcard host entry pointing at Traefik;
- a documentation page that says all of this in order.

This is the cheapest option by a wide margin and it is mostly a writing task. It does not
help the operator who has nothing in front, which is the operator the issue is about.

## Option D: no domain required

Tailscale Serve gives a tailnet node a publicly trusted `ts.net` certificate with no
domain and no DNS provider. Bloud removed the tailnet integration from the product; the
design is in `docs/plans/archive/tailnet-outpost.md`. This is a separate product decision
about remote access, not a TLS feature, and it should not be pulled in as a workaround
here.

## Side by side

| | A: Traefik DNS-01 | B: NPM as an app | C: document a terminator |
| --- | --- | --- | --- |
| Per-app route entries | none | none, if NPM uses one wildcard host | none, same trick |
| Operator steps to HTTPS | 3, in one UI | about 8, across two UIs | 4, one of them a config file |
| Bloud code | ACME render, one intent, one panel | catalog app, port handoff, contract for cert state | docs |
| Second admin UI | no | yes, port 81 | yes, the operator's own |
| DNS token owner | Bloud | NPM | the operator's proxy |
| Certificate expiry on the dashboard | free | needs NPM's API | no |
| Ports 80 and 443 | Traefik | NPM, Traefik moves | the operator's proxy |
| Backup story | one data directory | two state stores | outside Bloud |
| New maintenance risk | provider matrix | certbot plugin drift | none |
| Helps an operator who already runs a proxy | yes, still works | no, it conflicts | yes, this is the option |

## Recommendation

Ship A, and ship C as documentation now.

The comparison turns on one fact: the expensive part of HTTPS is the scheme contract, and
NPM does not remove it. Bloud already built that part. What is left is a resolver block,
one environment variable, one volume, and a settings panel. Option B spends more
orchestrator effort on port handoff than option A spends on ACME, and it leaves the
operator with two places to be wrong.

The step count is the user-visible version of the same argument. Option A: open Settings,
type the domain and the token, create two DNS records. Option B: install NPM, create its
account, add provider credentials in NPM, create a certificate, create a wildcard host,
point it at Traefik, then come back to Bloud and set the address and the trusted proxy
net. The instinct in "that may be overkill" is correct, and the reason is that NPM's value
is a UI for work Bloud can do in one field.

Suggested order:

1. Make the external terminator a first-class setting rather than an environment
   variable, and write the documentation page. Option C, and it unblocks people who
   already have a proxy.
2. DNS-01 with the provider allowlist, the Domain panel, the DNS record check, and the
   wildcard router change. Vaultwarden is the acceptance gate: it is the app that cannot
   work without HTTPS, and `apps/vaultwarden/INTEGRATION.md` already lists what it needs.
3. Automatic record creation for the apex and the wildcard, per provider, behind an
   explicit action.

## Open questions

1. Where does the DNS token live? `secrets.json` fits invariant 8 precedence and the
   backup path. The `settings` table is a plain key/value store and is not the place for
   a credential.
2. How large is the provider allowlist, and who supplies a live test domain for each
   entry? An untested provider should not appear in the UI.
3. What does a failed DNS order look like? A node in `ERROR` would be wrong: the
   instance is up, the certificate is not. A system warning plus a visible retry seems
   right, and it needs a decision about retry cadence.
4. Should the ACME CA default to staging in development and production in a release?
   `tls.md` proposes `BLOUD_ACME_CA=staging`.
5. Does the dashboard show certificate expiry as a system widget? Option A gets it for
   free, and it is the feature that makes a certificate failure visible before the
   certificate expires.
6. How does the UI explain `localhost` and `bloud.local` staying plain HTTP? The copy
   should say why, in one sentence, rather than disable a field silently.
7. Does TLS on mean the plain `web` entrypoint redirects, or stays reachable? A redirect
   is not a security boundary, and the LAN can still speak plain HTTP to the container
   ports. `tls.md` states this; the UI should not imply otherwise.

## Sources

- [Issue #159](https://github.com/d-buckner/bloud/issues/159), opened 2026-10-02,
  retitled 2026-10-14.
- [Traefik ACME certificates resolver](https://doc.traefik.io/traefik/https/acme/):
  DNS-01, lego, and CNAME support.
- [lego DNS providers](https://go-acme.github.io/lego/dns/) and the
  [lego feature list](https://go-acme.github.io/lego/index.html): more than 200 DNS
  providers, wildcard and SAN support, and CNAME support by default.
- [Let's Encrypt rate limits](https://letsencrypt.org/docs/rate-limits/), page updated
  2026-08-05: 50 certificates per registered domain every 7 days.
- [traefik/traefik#12109](https://github.com/traefik/traefik/issues/12109), 2025-09-29:
  an entrypoint-level resolver issues per-service certificates and does not generate the
  wildcard.
- [Scoped ACME DNS-01: CNAME delegation and acme-dns](https://www.bigiron.cc/guides/scoped-acme-dns-01-cname-delegation-and-acme-dns):
  the CNAME-delegation recipe for scoped tokens.
- [Nginx Proxy Manager releases](https://github.com/NginxProxyManager/nginx-proxy-manager/releases):
  v2.16.0 on 2026-09-24, certbot 5.8.0, the DNS plugin warning, and the removal of DNS
  credentials from disk after certbot runs.
- [Nginx Proxy Manager setup](https://nginxproxymanager.com/setup/) and
  [installation notes](https://deepwiki.com/NginxProxyManager/nginx-proxy-manager/2-installation-and-setup):
  SQLite by default in `/data`, MariaDB optional, admin UI on 81.
- [Nginx Proxy Manager certificates API](https://deepwiki.com/NginxProxyManager/nginx-proxy-manager/9.2-certificates-api):
  certificate CRUD, renewal, and DNS provider discovery.
- [`docs/plans/tls.md`](tls.md): the three-axis analysis, the no-client-enrollment
  constraint, and the phase plan this document reuses.
- [`apps/vaultwarden/INTEGRATION.md`](../../apps/vaultwarden/INTEGRATION.md): the app
  that proves the gap and the fix.
