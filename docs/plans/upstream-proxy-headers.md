> Status: accepted (implemented on this PR; move to `plans/archive/` once merged)

# Plan: Trust an upstream reverse proxy's forwarded headers

## Problem

An operator who terminates TLS in front of Bloud (nginx proxy manager, Caddy,
Traefik, Cloudflare Tunnel, any HTTPS reverse proxy) cannot complete the login
journey. After entering credentials at
`https://<host>/if/flow/default-authentication-flow/`, the Authentik flow never
advances.

### Root cause

Three facts combine:

1. **Bloud builds every URL with `http`.** `hostset.BaseURLFor` returns
   `http://localhost:8080` for `localhost` and `http://<host>` for everything
   else, and `HostSet.IssuerBaseURL` returns the primary host's base URL for a
   non-localhost primary. There is no scheme knob, by design:
   [the 2026-09-19 review](../specs/review-2026-09-19.md) records "hardcode `http`, which matches
   reality: invariant 10 says no TLS ships today".

2. **Traefik discards the proxy's `X-Forwarded-*` headers.** The generated
   static config (`services/host-agent/internal/appconfig/traefik.go`,
   `staticConfig`) intentionally emits no `forwardedHeaders` block, so Traefik
   keeps its secure default: it drops client-supplied `X-Forwarded-*` and sets
   its own. Traefik's own `X-Forwarded-Proto` describes the connection *to
   Traefik*, which is plain HTTP when a TLS terminator sits in front. Traefik
   therefore tells its backends the request was `http`.

3. **Authentik trusts Traefik and believes the `http` scheme.** Authentik only
   accepts forwarded headers from trusted proxy networks, and Traefik's container
   address in `apps-net`/`authentik-internal` falls inside Authentik's built-in
   defaults (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`). So Authentik
   accepts Traefik's `X-Forwarded-Proto: http` while the browser is on `https`.
   Authentik then interprets the HTTPS request as HTTP and generates `http://`
   URLs, which the HTTPS page blocks as mixed content. The flow stalls.

Authentik's own reverse-proxy guide documents exactly this symptom ("an endless
loading indicator, a generic authentication error, or a browser error about
blocked mixed content usually means that authentik is interpreting an HTTPS
request as HTTP") and requires the proxy to set `X-Forwarded-Proto` from the
original request.

### What is not the cause

- The browser's `Host` header is fine. Bloud derives OAuth URLs from the host
  set, and nginx proxy manager forwards the original `Host` by default.
- The redirect URI registration is fine. Once the domain is a configured host,
  `http://<host>/auth/callback` is registered in Authentik.
- Bloud's `http://` URLs are survivable while the proxy serves and upgrades port
  80. They are a separate, deferred problem (see Non-goals).

## Decision

Add one operator-configured trust scope: the addresses of the reverse proxy
directly in front of Traefik. When set, Traefik trusts `X-Forwarded-*` from
those addresses and forwards them, so the original scheme reaches Authentik.
Never `insecure: true`, which trusts any client.

## Interface

- **Env var**: `BLOUD_TRUSTED_PROXY_NETS`, comma-separated IPs or CIDRs of the
  proxy as seen from the Traefik container or host. Default empty.
- **Config field**: `config.Config.TrustedProxyNets []string`, parsed with the
  existing `splitNets` helper (invalid entries dropped, empty yields nil).
- **Traefik configurator**: `NewTraefikConfigurator` takes the list; when
  non-empty, `staticConfig` emits `forwardedHeaders.trustedIPs` on every
  entrypoint it writes (`web` and `web-local`).

Generated static config with the setting populated:

```yaml
entryPoints:
  web:
    address: ":80"
    forwardedHeaders:
      trustedIPs:
        - "192.168.1.7"
  web-local:
    address: ":8080"
    forwardedHeaders:
      trustedIPs:
        - "192.168.1.7"
```

Empty list emits no `forwardedHeaders` key, so the emitted config for every
existing deployment is byte-identical to today.

## Implementation steps

1. `services/host-agent/internal/config/config.go`: add the `TrustedProxyNets`
   field and `splitNets(getEnv("BLOUD_TRUSTED_PROXY_NETS", ""))` in
   `LoadWithLogger`.
2. `services/host-agent/internal/appconfig/traefik.go`: add the field and
   constructor parameter; render the block in `staticConfig`. Quote each entry
   (`"10.0.0.0/8"`) so YAML never misreads a scalar.
3. `services/host-agent/internal/appconfig/register.go`: pass
   `cfg.TrustedProxyNets` into `NewTraefikConfigurator`.
4. `services/host-agent/internal/appconfig/traefik_test.go`: extend the
   `staticConfigShape` decode to read `trustedIPs`; assert it is absent when the
   list is empty, present on every entrypoint when set, and that `insecure`
   never appears.
5. Docs: add the variable to the key-env list in `AGENTS.md` and note the
   external-proxy case in invariant 10.

## Security analysis

- The trust scope is a source CIDR, not a header. A request whose TCP peer is
  outside the list keeps Traefik's secure default: its `X-Forwarded-*` are
  overwritten. This preserves the PR 4 fix (`forwardedHeaders.insecure: true`
  removed).
- An attacker able to source traffic from a listed address can spoof
  `X-Forwarded-Proto`/`Host` downstream. The list must therefore contain only
  the proxy, never a client network, and never when Traefik is directly exposed.
- host-agent is unaffected: it reads neither `X-Forwarded-*` nor client IP
  (`requestHost` uses `r.Host`; `isLocalRequest` uses `r.RemoteAddr`). The
  forward-auth middlewares already trust whatever Traefik sets, and Traefik's
  set now includes the trusted upstream, which is the intent.
- Authentik trusts the source that matters here (Traefik's container address)
  under its defaults. A deployment whose podman/container subnet falls outside
  `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` must also set
  `AUTHENTIK_LISTEN__TRUSTED_PROXY_CIDRS` to include it, or Authentik will
  ignore Traefik's headers.

## Failure modes

| Condition | Result |
|---|---|
| Variable unset | Byte-identical static config; dev, e2e, and native backends unaffected. |
| Variable set to the wrong address | Symptom unchanged: Authentik still sees `http`. |
| Variable set to a client network | Any client can spoof its scheme and host downstream. |

## Non-goals

- **HTTPS URL generation.** `hostset` still answers `http://` for every host, so
  the browser is bounced to `http://<host>` and the proxy must serve and upgrade
  port 80. Removing that dependency requires splitting the browser-facing scheme
  from the container-facing issuer (`http://<primary>` for app containers) and
  touches Authentik provisioning and every native-oidc app config. It is the
  remaining half of invariant 10's TLS story and deserves its own plan.
- **TLS at Traefik.** Bloud ships no certificate resolver and no ACME story.
- **Tailscale Serve.** Covered by [the tailnet outpost plan](tailnet-outpost.md).

## Verification

- Unit: `go test ./internal/appconfig/...` plus `./internal/config/...`.
- Generated config parses as YAML and carries `trustedIPs` only when configured.
- Live: with a TLS-terminating proxy and the variable set, `https://<host>/`
  completes login; Authentik's flow advances past credential submission. Confirm
  the scheme Authentik sees at `https://<host>/api/v3/admin/system/`.
- Regression: `./bloud dev` and `./bloud e2e lifecycle` with the variable unset.

## Rollout

Documented as an operator setting; no default change, no migration. Existing
deployments see no behavioral difference.
