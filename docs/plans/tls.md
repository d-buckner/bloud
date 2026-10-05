> Status: draft

# Plan: HTTPS with publicly trusted certificates

## Problem

Bloud serves plain HTTP everywhere. The generated Traefik static config
(`services/host-agent/internal/appconfig/traefik.go`, `staticConfig`) declares two
entrypoints, `web` (`:80`) and `web-local` (`:8080`), and no TLS section: no
`:443`, no `certificatesResolvers`, no certificate handling. `hostset.BaseURLFor`
(`services/host-agent/internal/hostset/hostset.go`) hardcodes the scheme to
`http://` for every host. The only TLS in the repo is Tailscale Serve
(`services/host-agent/internal/sharing/serve_config.go`), which terminates at a
tailnet node with a `ts.net` certificate.

Two deployments are wanted:

1. **Local TLS.** The instance runs at home. The operator wants `https://` on the
   LAN without depending on a public HTTPS reverse proxy they run separately.
2. **TLS behind an external proxy.** Caddy, nginx proxy manager, Traefik-in-front,
   or a Cloudflare Tunnel terminates TLS and forwards to Bloud.

Both are blocked by the same missing piece, and one product constraint decides the
whole design.

## The constraint: no client-side certificate enrollment

**Nothing Bloud ships may require an end user to install a CA certificate in their
browser, OS trust store, or device.**

This rules out the local CA (mkcert-shaped) design outright. That design is the
usual cheap answer to "TLS at home": generate a root CA, issue LAN certificates
from it, install the root on every device. It fails here because:

- Every device on the network must enroll before first use. Phones, tablets, TVs,
  and game consoles make that painful or impossible.
- Bloud would hold the private key of a CA that every client trusts. A compromise
  of that key is a compromise of every user's traffic to every host the CA signed,
  not just this instance.
- Un-enrolled devices get a hard browser interstitial, which turns "enable TLS"
  into "some devices now cannot reach the dashboard".

What survives is the branch where every certificate is trusted by a stock browser.
That has a consequence worth stating plainly, because it shapes the product story:

> **Local TLS requires the operator to bring a real domain.** `bloud.local` can
> never be HTTPS: `.local` is reserved for mDNS, no public CA issues for it, and
> Bloud removed its mDNS announcer (invariant 10). `localhost` can never be
> HTTPS either: no CA issues for a special-use name.

The honest pitch is "bring a domain and point its DNS at your LAN address". That
is a well-trodden homelab pattern, and it is strictly better than the local CA
because the trust comes for free.

The payoff for the architecture is larger than the cost. Under this constraint
every certificate is publicly trusted, which means **app containers verify the
OIDC issuer with no CA bundle distribution**. The alternative design would have
required a per-app trust story (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`,
`REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`) for every image in the
catalog, with each wrong value surfacing as a confusing TLS error deep inside the
app. That whole class of work is off the table. Bloud also never holds a CA key,
only leaf keys.

## Three axes, and only one of them is shared work

TLS is three separable decisions. Conflating them is what makes this confusing.

| Axis | Question | Local TLS | Behind Caddy |
|---|---|---|---|
| **Termination** | who holds the leaf key and does the handshake | Traefik | the external proxy |
| **Trust** | who issued the cert and who must trust it | public CA via ACME DNS-01 | public CA via the proxy |
| **Scheme contract** | what URL Bloud tells the browser, the identity provider, and each app container | `https://<host>` | `https://<host>` |

The two cases differ on the first two axes and are **identical on the third**. The
third axis is where the work is. Build it once, behind a default that keeps `http`,
and both cases become configuration on top.

## The shared blocker: the scheme contract

Every generated URL flows from `HostSet.BaseURLFor` through `BaseURLs`,
`AllBaseURLs`, and `IssuerBaseURL`. Because those return `http://`, four things
break the moment a browser is on `https://`:

| Consumer | Where | What breaks |
|---|---|---|
| OAuth redirect URIs | `services/host-agent/internal/sso/oidc.go`, `OIDCInputsForApp` registers `appSubdomainURL(baseURL) + callbackPath` per base URL | Registered as `http://jellyfin.<host>/...` while the browser is on `https://`: `redirect_uri_mismatch`, login dies at the provider |
| Dashboard OAuth app and `meta_launch_url` | `services/host-agent/internal/sso/blueprint.go` | Launch links bounce the browser off the secure origin |
| OIDC issuer | `HostSet.IssuerBaseURL`, baked into every native-oidc app config and used for discovery | The issuer string says `http://`; strict clients reject it |
| Authentik's own generated URLs | the flow documented in [`upstream-proxy-headers.md`](archive/upstream-proxy-headers.md) | Authentik reads the request as HTTP and emits `http://` URLs that an HTTPS page blocks as mixed content |

`apps/vaultwarden/INTEGRATION.md` already proves this is the whole gap. An
unmodified Vaultwarden behind a Traefik TLS entrypoint with a self-signed
certificate and `DOMAIN=https://...` passes the entire flow. That integration doc
lists what Bloud would need: a TLS entrypoint, a scheme-aware base URL ("it is
`http://` throughout `hostset`"), the HTTPS callback registered with the
provider, and a browser-trusted certificate. The first three are this plan. The
fourth is what the no-CA constraint pins to a public CA.

## One issuer, served through the terminator

A design point that falls out of OIDC and is worth fixing early: the issuer URL is
a single string used by both the browser (front channel) and the app server (back
channel discovery). It cannot differ per audience, because a client that saw one
issuer in the front channel rejects a token minted by another.

So when the browser-facing scheme is `https`, the container-facing issuer is the
same `https://` URL, and app containers reach it **through the TLS terminator**.
This works without changing `IssuerExtraHost`:

- **Behind Caddy:** the container resolves `<primary>` via `host-gateway` to the
  host machine, where Caddy listens on 443 and presents a valid public cert.
- **Traefik ACME:** the container resolves `<primary>` via `host-gateway` to the
  host machine, where Traefik's `websecure` entrypoint presents a valid public
  cert.

The existing `extraHosts: <issuer>:host-gateway` mechanism needs no change,
because the certificate validates on the name, not on where the name resolves.
One exception: if the external proxy runs on a different machine than the host
agent, `host-gateway` is wrong and real DNS must resolve the issuer name. The
existing `BLOUD_SSO_ISSUER_URL` override covers that case.

## Phase 1: scheme-aware host set (shared prerequisite)

Add a browser-facing scheme to the host set and derive every URL from it.

- `hostset.HostSet` gains a scheme field, defaulting to `http` so no existing
  deployment changes behavior.
- `BaseURLFor` returns `scheme://<host>` for non-localhost hosts. `localhost`
  keeps `http://localhost:8080`: dev and e2e parity depends on it, and under this
  constraint `localhost` cannot get a trusted certificate anyway, so the exception
  is forced rather than a compromise.
- `IssuerBaseURL`, `BaseURLs`, `AllBaseURLs`, and the forward-auth
  `external_host` all follow automatically, since they already route through
  `BaseURLFor`.

  > **Corrected.** `AllBaseURLs` does not follow. Its detected local-IP entries
  > are deliberately not derived from the primary host: they stay plain http on
  > the entrypoint port, because a LAN client reaching the box by address has no
  > TLS terminator in front of it. See the "Follow-up: the LAN IP entries
  > inherited the public scheme" section of
  > [`proxied-scheme-urls.md`](archive/proxied-scheme-urls.md).
- The scheme is instance-wide, not per host. One instance is served one way: the
  same Traefik or the same upstream proxy serves every host in the set. Per-host
  schemes would allow a state no topology produces and would make `IssuerBaseURL`
  ambiguous.
- Port handling stays default-per-scheme (`https://<host>` means 443). The odd-port
  case is already served by the `BLOUD_SSO_BASE_URL` full-URL override, which pins
  a base URL verbatim.

One precedence trap governs the wiring: `hostset.Resolve` applies
`BLOUD_SSO_BASE_URL` only when no stored hosts exist. Once an admin has saved
hosts, the env knobs are ignored entirely. The scheme setting must be stored, not
env-only, for the same reason admin hosts are stored: env seeds initial state, the
admin UI wins.

## Phase 2: external terminator (Caddy and friends)

Topology: `browser -> https://<host> (Caddy, public cert) -> http://traefik:80 -> apps`.

Already shipped: `BLOUD_TRUSTED_PROXY_NETS` makes Traefik emit
`forwardedHeaders.trustedIPs` on both entrypoints, so the upstream proxy's
`X-Forwarded-Proto` survives the hop and Authentik stops reading HTTPS as HTTP.
That was the login-stall fix, and it is the only TLS-adjacent piece in the tree
today.

What this phase adds:

1. Phase 1's scheme set to `https`, so redirect URIs, launch URLs, and the issuer
   are `https://` while the internal hop stays plain HTTP.
2. A documented proxy contract: forward `Host`, `X-Forwarded-Proto`, and
   `X-Forwarded-For`; do not rewrite the scheme; cover `*.<host>` for app
   subdomains via DNS wildcard or on-demand TLS.
3. A deployment requirement that Bloud not be reachable on `:80` directly from
   the LAN, or TLS is bypassable. The proxy owns 80 and 443; Traefik binds only
   the private network the proxy talks to.
4. Authentik trusted-proxy CIDR must include Traefik's address. Already true under
   Authentik's RFC1918 defaults; the escape hatch is
   `AUTHENTIK_LISTEN__TRUSTED_PROXY_CIDRS`.

What Bloud never has to care about in this mode: certificates, renewal, TLS
versions, cipher suites, HSTS, port 443. All of it belongs to the proxy.

## Phase 3: Traefik ACME DNS-01 (local TLS with a brought domain)

Topology: `browser -> https://<host> (Traefik, public cert) -> apps`, served on
the LAN only, with no inbound connectivity from the internet.

DNS-01 is the only ACME challenge that fits. HTTP-01 requires port 80 reachable
from the public internet, which contradicts a LAN-only instance. DNS-01 needs a
DNS provider API token and nothing else: no port forwarding, no public address.

- `staticConfig` gains a `websecure` entrypoint on `:443`, a
  `certificatesResolvers` block using the DNS-01 challenge, and an HTTP-to-HTTPS
  redirection on the `web` entrypoint.
- Provider credentials reach Traefik through container **environment variables**,
  never through `traefik.yml`. The generated static config is written 0644 today
  and is a reasonable thing to read in a support bundle; a DNS API token is not.
  Traefik reads provider credentials from the environment, so naming the provider
  in the resolver config and supplying the token via env is both the documented
  pattern and the safe one.
- The ACME account key and issued certificates live in the Traefik data directory
  under `BLOUD_DATA_DIR`. Traefik renews them itself. The reconciler's job is to
  configure the resolver idempotently and to not clobber the ACME storage, not to
  manage certificates.
- Use Let's Encrypt's staging CA during development (`BLOUD_ACME_CA=staging`) to
  avoid burning production rate limits while testing.
- Reuse `BLOUD_AUTHENTIK_ADMIN_EMAIL` for the ACME registration address when no
  dedicated `BLOUD_ACME_EMAIL` is set.

## Security analysis

- **No CA key, ever.** Bloud holds leaf keys only. The no-enrollment constraint is
  what makes this true, and it is the single largest security win of the design.
- **DNS token scoping is the sharp edge.** A token that can edit a whole DNS zone
  is a meaningful secret to hand a home server. Recommend CNAME delegation: point
  `_acme-challenge.<host>` at a throwaway zone (for example
  `acme-challenge.<host>`) and give Bloud a token scoped to that zone only. A
  leaked Bloud token then cannot touch the real zone. lego and most providers
  follow the CNAME automatically, so this costs the operator one DNS record.
- **`BLOUD_TRUSTED_PROXY_NETS` stays a source-address scope, not a header scope.**
  A peer outside the list keeps Traefik's secure default and its `X-Forwarded-*`
  is overwritten. The list must contain only the proxy, never a client network,
  and never when Traefik is directly exposed. This preserves the PR 4 fix that
  removed `forwardedHeaders.insecure: true`.
- **Plaintext HTTP must not remain reachable** in either mode. In phase 2 that is
  the operator's firewall job. In phase 3 Traefik redirects `:80` to `:443`, but
  the redirect is not a security boundary: a client that speaks plain HTTP to the
  app port on the LAN still gets plain HTTP. Both modes assume a trusted LAN for
  the direct container ports, which is the same assumption the rest of Bloud makes.
- **The scheme is a trust decision, so it is admin-only.** It changes OAuth
  redirect URIs in the identity provider. It goes through the orchestrator
  (`SetHostsIntent` or a sibling intent), never a direct store write, per
  invariant 1.

## Verification

- **Unit.** `hostset` scheme tests (default `http`, `https` for non-localhost,
  `localhost` exception, issuer derivation). `sso/blueprint_test.go` assertions
  that redirect URIs and the issuer carry `https://` when the scheme is set.
  `appconfig/traefik_test.go` static-config shape: `websecure` present, resolver
  present, redirection present, and no provider credential ever appearing in the
  emitted YAML.
- **Acceptance test: Vaultwarden.** It is the app that cannot work without HTTPS,
  so it is the gate. Run it over a real HTTPS origin and confirm
  `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP` is no longer needed. This is already
  verified by hand in `apps/vaultwarden/INTEGRATION.md`; the plan makes it a
  repeatable assertion.
- **Live, phase 2.** Caddy in front with a real certificate and
  `BLOUD_TRUSTED_PROXY_NETS` set to Caddy's address: complete the dashboard login
  and one app login through `https://`, and confirm Authentik sees
  `X-Forwarded-Proto: https`.
- **Live, phase 3.** A real test domain with DNS-01 against the staging CA:
  confirm a certificate is issued, renewal is configured, and the whole login
  journey completes over HTTPS with no inbound port 80 from the internet.
- **Regression.** With the scheme left at its `http` default, the emitted Traefik
  config is byte-identical to today and `./bloud dev` plus
  `./bloud e2e lifecycle` pass unchanged.

## Rollout

Default scheme `http`, default TLS mode none. No migration beyond a schema
migration for the stored scheme setting (`services/host-agent/internal/schema/`
ledger). Existing deployments see no behavioral difference until an operator
enables TLS. Ship phase 1 first, on its own, because it is the risky shared
change; phases 2 and 3 are configuration layered on top and can land
independently.

## Out of scope

- **Local CA and self-signed certificates.** Ruled out by the no-enrollment
  constraint, not deferred.
- **TLS on `bloud.local` or `localhost`.** Not achievable with public trust.
- **HSTS policy, TLS version pinning, cipher tuning.** Deployment-level knobs; the
  default should be sane, but a policy UI is not part of this.
- **Tailscale Serve.** Already provides publicly trusted `ts.net` certificates for
  remote access. The tailnet integration that used it was removed from the
  product; the design is kept in
  [`tailnet-outpost.md`](archive/tailnet-outpost.md) and is compatible with
  this design should the feature return.
- **Automatic DNS provider detection or a certificate management UI.** Enabling
  TLS means naming a provider and supplying a scoped token.
