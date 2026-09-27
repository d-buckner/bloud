> Status: accepted. Parts A, B, and E are implemented. The TLS-at-Traefik work
> that this plan pins down is still open and is a non-goal here.

# Plan: Make derived URLs survive a TLS-terminating proxy

## Problem

An operator who puts `https://mydomain.com` in front of Bloud cannot log in, and
other things break in ways that are harder to see. [The upstream proxy headers
plan](upstream-proxy-headers.md) fixed one of the three layers. The other two are
open, and they are the hard ones.

The failure is a class, not a bug. Bloud derives every externally visible URL from
a model that assumes a flat, plain-HTTP topology with nothing in front of it. Add
one hop and several derived values become wrong.

### Layer 1: Traefik discarded the upstream scheme. LANDED.

Fixed in #110. `BLOUD_TRUSTED_PROXY_NETS` makes Traefik accept `X-Forwarded-*`
from the proxy directly in front of it, so the original scheme survives one more
hop to Authentik. Pinned by `internal/appconfig/traefik_test.go`: `insecure: true`
is never emitted, `trustedIPs` lands on every entrypoint, and an empty list leaves
the generated file byte-identical.

This layer is done. Do not reopen it.

### Layer 2: the scheme is hardcoded, and the admin UI cannot express https

`hostset.HostSet.BaseURLFor` returns `http://localhost:8080` for `localhost` and
`http://<host>` for everything else. There is no scheme in the model. Everything
downstream inherits the literal:

| Derived value | Where |
|---|---|
| OIDC issuer base URL | `HostSet.IssuerBaseURL` |
| OAuth redirect URIs | `sso/oidc.go`, one per base URL |
| Dashboard launch URLs | `OIDCInputsForApp` |
| Outpost browser URL | `outpostBlueprintTemplate`, `authentik_host_browser` |
| Forward-auth external host | `generateForwardAuthBlueprint` |

There is a partial escape hatch. `BLOUD_SSO_BASE_URL` is parsed and stored as a
per-host URL override, so `BLOUD_SSO_BASE_URL=https://mydomain.com` does carry a
scheme. But `Resolve` applies that override **only in the `len(in.Stored) == 0`
branch**. Once an admin adds a custom domain through Settings to Hosts, stored
hosts win over env entirely (invariant 9), `New()` returns an empty override map,
and **https cannot be expressed at all**. The knob that exists is unavailable on
the path a real operator takes.

### Layer 3: the browser-facing scheme and the container dial plan are one value

This is the constraint that makes the fix non-trivial, and the reason a test with
one vantage point cannot see the problem.

OIDC requires the `issuer` string to be byte-identical on every hop. The browser
and the app container must both be told the same thing, and both must be able to
reach it.

Today `HostSet.IssuerExtraHost()` returns `<issuerHost>:host-gateway`, which pins
the issuer hostname to the host gateway so a container reaches Traefik directly
and skips the outside world. That is deliberate: it avoids hairpinning through a
router and avoids needing split-horizon DNS. It also means the container speaks
**plain HTTP** to Traefik on the issuer hostname.

Now set the browser-facing issuer to `https://mydomain.com`. The container dials
`https://` at the host gateway, which lands on port 443, where Traefik serves
nothing, because Bloud ships no certificate resolver and no ACME story. Discovery
fails. The two roles are welded together:

```
browser  -> https://mydomain.com  -> upstream proxy -> Traefik :80   (TLS ends at proxy)
container -> https://mydomain.com -> host-gateway :443               (nothing listening)
```

So a https issuer is **not deployable today**, and no amount of scheme plumbing
changes that. Either TLS terminates at Traefik too, or the container must reach the
issuer through the same upstream proxy, which reintroduces the hairpin and DNS
problems the `host-gateway` pin exists to avoid.

The useful conclusion: the model must represent these as two roles, and Bloud must
say out loud when the pair is undeployable, rather than emit a URL that stalls a
login at runtime.

## Decision

Do not ship a magic scheme knob. Ship three smaller things that make the next step
safe and make the current trap visible.

1. **Represent the scheme.** Put scheme in `HostSet` as a first-class per-host
   value that works for stored hosts, not just through the legacy env override.
   Default `http`, so every existing deployment renders byte-identical URLs.
2. **Name the deployability constraint in code.** A diagnostic that reports when
   the derived issuer cannot actually be served from both vantage points. Startup
   says so instead of the login flow failing mysteriously.
3. **Test the derivation, the artifacts, and the failure modes.** That is what
   this plan is mostly about, because the eventual TLS fix is a design decision
   that needs a floor to land on.

## Interface

```go
// Scheme is the protocol a host is served under.
type Scheme string

const (
    SchemeHTTP  Scheme = "http"
    SchemeHTTPS Scheme = "https"
)
```

`HostSet` gains a per-host scheme map. Absent means `SchemeHTTP`, which is what
every host means today.

| Method | Purpose |
|---|---|
| `SchemeFor(host) Scheme` | The scheme one host is served under. `http` unless set. |
| `WithScheme(host, scheme) HostSet` | Copy with a host's scheme pinned. |
| `PublicScheme() Scheme` | The primary host's scheme. The deployment's scheme. |
| `Deployability() []Issue` | Why the derived URLs cannot be served, if that is true. |

`Resolve` gains `Input.PublicScheme`, wired from `BLOUD_PUBLIC_SCHEME`. Empty means
`http`. This is the path that works for stored hosts, which the legacy
`BLOUD_SSO_BASE_URL` override does not.

`BaseURLFor` becomes scheme-aware and keeps the localhost dev convention only for
plain HTTP, since `http://localhost:8080` is a dev VM port forward, not a rule
about TLS.

## Test strategy

The class of bug is cross-boundary derivation, so no single vantage point is
enough. Three layers, cheapest first.

### A: derivation table (unit)

Table-drive every derived value across the topologies that matter:

| Topology | Public scheme | Notes |
|---|---|---|
| Dev VM | `http`, localhost primary | The `:8080` convention |
| Direct, no proxy | `http`, real domain | Today's default |
| TLS-terminating proxy | `https`, real domain | The reported case |
| Proxy on a non-standard port | `https` on `:8443` | Via URL override |
| Stored custom host + https | `https` | The Layer 2 gap |

For each: issuer, redirect URIs, launch URL, outpost browser URL, extra_hosts
entry, and the deployability verdict.

A is cheap and catches wrong derivation. It cannot catch Authentik disagreeing,
mixed content, or the hairpin. That is what B and the diagnostic are for.

### B: golden artifacts

Reuse the `traefik_test.go` pattern, which decodes the generated YAML into a typed
shape and asserts on the decoded values rather than substring matching. Apply it to
the Authentik templates for all three strategies, since each fails differently
behind a proxy:

| Strategy | What goes wrong behind a proxy |
|---|---|
| `native-oidc` | Issuer mismatch; the browser blocks mixed-content discovery |
| `forward-auth` | `authentik_host_browser` is http, so the redirect leaves TLS |
| `ldap` | Launch URL and app-side redirect are http |

Assert that no browser-visible field carries `http://` when the public scheme is
`https`, and that container-internal URLs are allowed to. The allowlist is not
noise: it is the documentation of the browser versus container split.

### E: failure signatures and red-run canaries

This is what makes the suite trustworthy rather than decorative.

**Name the broken layer.** Never assert "login worked". Assert the specific
observable that distinguishes the layers, so a red test says which component is
wrong:

| Symptom | Layer at fault |
|---|---|
| `Deployability()` reports the issuer scheme mismatch | Layer 3, dial plan |
| Blueprint carries `http://` in a browser field | Layer 2, derivation |
| Traefik config has no `trustedIPs` while a proxy is configured | Layer 1, trust scope |
| Redirect URI scheme differs from the public scheme | Layer 2, registration |

**Prove the tests are not vacuous.** A test that passes both with and without the
fix is worse than no test. Two permanent negative controls:

1. With the public scheme forced to `http` while a proxy is configured, the suite
   must still report the mismatch. This pins that the operator setting is still
   required and that nothing silently papered over it.
2. With a https issuer and no TLS at Traefik, `Deployability()` must report the
   undeployable pair. If a future change "fixes" this by quietly downgrading the
   issuer to http, the B assertion that browser-visible fields are https fails.
   The two tests together close off the wrong fix.

## What is implemented

| Part | Where |
|---|---|
| Scheme model, `SchemeFor`, `WithScheme`, `WithPublicScheme`, `Deployability`, `ProxyConsistency` | `services/host-agent/internal/hostset/hostset.go` |
| A: derivation table, default-unchanged guard, builtin and override rules | `services/host-agent/internal/hostset/scheme_derivation_test.go` |
| B: blueprint assertions for all three strategies, decoded from YAML | `services/host-agent/internal/sso/blueprint_scheme_test.go` |
| E: four canaries plus the failure-signature table | `services/host-agent/internal/hostset/proxy_canary_test.go` |
| Layer 1, already landed in #110 | `services/host-agent/internal/appconfig/traefik_test.go` |

`BLOUD_PUBLIC_SCHEME` reaches `HostSet` through `Resolve.Input.PublicScheme` but is
not yet wired into `config` or startup. A reachable knob without TLS at Traefik
produces a broken deployment, and TLS at Traefik is a non-goal below. The model
and its tests are the floor the real fix lands on.

## Non-goals

- **TLS at Traefik.** No certificate resolver and no ACME story in this plan. It
  is the prerequisite for a deployable https issuer and deserves its own plan.
- **Real proxy in the e2e harness.** The full reproduction (a TLS-terminating
  Caddy, a locally minted wildcard cert, Chromium `--host-resolver-rules`) is the
  right next step after A, B, and E land. It is deliberately out of scope here so
  the cheap layers get pinned first.
- **Cloudflare Tunnel and Tailscale Serve.** Different header and trust quirks
  each. The honest coverage matrix is four topologies times three strategies, and
  only the direct row is covered today.
- **Store and UI changes.** The scheme reaches `HostSet` through `Resolve`.
  Persisting a per-host scheme in the `hosts` table needs a migration and a UI
  control, which is a follow-up once the derivation is pinned.

## Verification

- `go test ./internal/hostset/...` covers A and the diagnostic.
- `go test ./internal/sso/...` covers B.
- `go test ./internal/appconfig/...` keeps Layer 1 pinned.
- `./bloud validate --tier fast` for lint, formatting, prose, and headers.
- `./bloud validate --tier integration` for the real install path, now that it
  runs in CI.
