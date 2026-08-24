# Plan: Project-Wide Self-Signed TLS (local CA) + AppFlowy SSO

## Problem

Bloud's SSO issuer (Authentik behind Traefik) is plain HTTP. This blocks real SSO
for apps whose auth layer requires a server-side OIDC client against the issuer —
AppFlowy is the concrete case:

- AppFlowy's cloud API only accepts JWTs minted by its bundled GoTrue, so
  proxy-level forward-auth (Navidrome's pattern) cannot grant API access.
- The GoTrue fork (`appflowyinc/gotrue:0.17.12`) supports admin-registered
  **custom OIDC providers** — exactly the "sign in with Bloud" button we want —
  but its schema enforces an HTTPS issuer (verified in the image):

  ```sql
  constraint custom_oauth_providers_oidc_issuer_https check (
      provider_type != 'oidc' or issuer is null or issuer like 'https://%'
  )
  ```

  (migration `20260219120000_add_custom_oauth_providers.up.sql`)

The user's expectation: Bloud apps should authenticate through Bloud SSO so
identities stay in sync with Authentik instead of living in a second store.

Scope decision: rather than TLS only for `sso.*`, serve the **whole project**
behind a self-signed local CA. The SSO issuer is the first consumer that
*requires* it; everything else (apps, dashboard, WebSocket upgrades) benefits
from the same certificates, and the tailnet/remote plan (`persistent-ports-fqdn-remote.md`)
also needs a stable HTTPS issuer.

## Verified evidence (2026-08-24, live dev VM)

Everything below was probed against the running stack, not inferred:

| Fact | Evidence |
|---|---|
| `POST /admin/custom-providers` exists in gotrue | 400 `validation_failed: provider_type must be either 'oauth2' or 'oidc'` with admin Bearer token (unauth → 401) |
| Gotrue admin creds are known to Bloud | `GOTRUE_ADMIN_EMAIL`/`GOTRUE_ADMIN_PASSWORD` derived via `sso.DeriveSecret` (main.go), present in container env; password grant works |
| OAuth browser routes live | `GET /authorize` → 400 (params required), `GET /callback` → 303 |
| Web renders custom providers with admin display names | `src/application/types.ts` (`CustomAuthProvider`, `CUSTOM_PROVIDER_PREFIX`), `LoginProvider.tsx` (named buttons), providers fetched from cloud `GET /api/server-info/auth-providers` → `custom_providers: [{identifier, name}]` |
| SAML is a dead end in this build | Web has a SAML dialog calling `POST /sso`, but `/sso`, `/sso/authorize`, `/sso/redirect` all 404 in the binary; no `/admin/sso*` routes either |
| Only server-side calls need HTTPS | Discovery, JWKS fetch, and token exchange happen inside the gotrue container; the browser auth redirect and callback are ordinary browser navigations (HTTP would work, but the issuer string is stored/validated as https) |
| Go TLS trust is overridable | Go's `crypto/x509` honors `SSL_CERT_FILE` — no image rebuild needed to trust a local CA |
| Traefik static config is generated | `internal/appconfig/traefik.go` `PreStart` writes `traefik.yml` (single `web` entrypoint on :8080) + `dynamic/base.yml`, `dynamic/authentik-routes.yml`; app routes in `dynamic/apps-routes.yml` |
| 1-seat free license is unchanged by SSO | "Seat limit reached: counted 1 Member/Owner users across initialized workspaces (limit 1)" — seats are per-user regardless of auth method |

## Solution

Bloud generates and owns a **local certificate authority** at bootstrap. Traefik
serves every route over a TLS entrypoint (`:8443`) in addition to the existing
HTTP entrypoint (dual-protocol; HTTP stays valid so untrusted browsers and
existing tooling keep working). The SSO issuer becomes
`https://sso.localhost:8443`, which AppFlowy's gotrue consumes as a custom OIDC
provider. Users get a "Sign in with Bloud" button in AppFlowy's login screen;
accounts are created on first SSO login, keyed by the Authentik email.

### Certificate bootstrap

- `init-secrets` (or system bootstrap, whichever runs first) generates:
  - **Bloud CA**: ECDSA P-256, self-signed, 10-year validity. Stored at
    `<dataDir>/tls/ca.crt` + `ca.key` (0600).
  - **Leaf cert**: signed by the CA, SANs `localhost`, `*.localhost`,
    `127.0.0.1`, `::1` (covers every app subdomain and the issuer).
    `<dataDir>/tls/server.crt` + `server.key`.
- Certificates are **generated once and never regenerated** — browser trust
  pins the CA fingerprint, so stability matters more than expiry. Rotation is a
  manual, documented operation (issue # TBD), not a reconcile behavior.
- A merged trust bundle (`<dataDir>/tls/ca-bundle.crt` = system bundle + Bloud
  CA) is what containers receive via `SSL_CERT_FILE`, so they keep validating
  public CAs too.
- Host browser trust is a one-time dev setup step (`./bloud setup` / `./bloud dev`
  detects missing trust and offers to install: `security add-trusted-certificate`
  on macOS, `update-ca-certificates` / certutil on Linux). Documented in AGENTS.md.

### Traefik

`staticConfig()` gains a second entrypoint:

```yaml
entryPoints:
  web:
    address: ":8080"
    forwardedHeaders: { insecure: true }
  websecure:
    address: ":8443"
    http:
      tls:
        certificates:
          - certFile: /certs/server.crt
            keyFile: /certs/server.key
```

- `<dataDir>/tls/{server.crt,server.key}` mounted into the Traefik container.
- Every existing router gets `tls: true` on the `websecure` entrypoint (HTTP
  routers unchanged). App routes in `apps-routes.yml` are regenerated the same
  way — the orchestrator's route generator adds the TLS field.
- **Open item (verify in Phase 1):** Authentik's OIDC issuer over the TLS
  host. 2025.x derives the issuer from the request Host by default
  (`issuer_mode: default`); if the served issuer doesn't match
  `https://sso.localhost:8443`, set `issuer_mode: explicit` +
  `issuer_url` on the OIDC provider via the API (the authentik configurator
  already manages providers).

### Public URLs — the "whole project" part

Single source of truth: the scheme comes from one config value,
`BLOUD_PUBLIC_SCHEME` (`http` | `https`, default `https` once the CA is
installed, `http` as the explicit fallback for untrusted environments).
`appSubdomainURL`/`appWSSubdomainURL` (main.go) compose scheme + port
(8443) + subdomain, so every derived URL — app public URLs, WebSocket URLs
(`wss://<app>.localhost:8443/ws/v2`), SSO issuer — changes in one place.

Per-app consequences of an HTTPS public URL (Phase 3, per-app follow-ups):

- **Redirect URIs registered with Authentik** (AFFiNE, Immich, and any future
  native-oidc app) must be re-registered with the `https` scheme — the
  per-app configurators own this, and they reconcile on every cycle, so the
  change is self-healing once the scheme flips.
- **WebSocket/realtime URLs** flip `ws://` → `wss://` (AppFlowy, and any app
  with real-time features).
- **Apps that fetch server-side** (e.g. gotrue → issuer, cloud → search) need
  the CA bundle: one template var `{{appCaBundlePath}}` + `SSL_CERT_FILE`
  (Go), equivalent for other runtimes, wired per app.
- **e2e + integration tests**: in-VM test binaries get `SSL_CERT_FILE` from
  the data dir; Playwright on the host needs no `ignoreHTTPSErrors` because
  the host trusts the CA (assert trust during `./bloud e2e` setup).

### AppFlowy SSO (first consumer, Phase 2)

Configurator `PreStart` (idempotent, per reconcile):

1. Ensure the Authentik OAuth2 application for AppFlowy exists (existing
   pattern: "Bloud OAuth2 Provider" + per-app applications, managed with the
   bootstrap token) with callback = the gotrue callback URL on the public
   origin.
2. Register the custom OIDC provider in gotrue via
   `POST /gotrue/admin/custom-providers` (admin Bearer from the derived admin
   creds), idempotent by `identifier`:

   ```json
   {
     "provider_type": "oidc",
     "identifier": "bloud-sso",
     "name": "Bloud SSO",
     "issuer": "https://sso.localhost:8443",
     "client_id": "<from the authentik application>",
     "client_secret": "<from the authentik application>",
     "scopes": ["openid", "email", "profile"],
     "attribute_mapping": { "email": "{claims.email}" }
   }
   ```

   (Exact field names/`discovery_url` override confirmed against the
   migration schema in Phase 2; the endpoint rejects unknown shapes with 400.)
3. `metadata.yaml` additions to the gotrue container: `extraHosts:
   sso.localhost:host-gateway` (reaches Traefik's TLS entrypoint via the VM
   host, same pattern as the native-oidc apps) and `SSL_CERT_FILE` pointed at
   the mounted CA bundle.

Resulting user journey: AppFlowy login screen → "Continue with Bloud" →
Authentik (already-logged-in Bloud session → immediate redirect; otherwise the
normal Bloud login) → gotrue callback → AppFlowy session. The GoTrue account
is created on first SSO login with the verified Authentik email
(`GOTRUE_MAILER_AUTOCONFIRM=true` already set, so no email confirmation).

What this does and does not change:

- ✅ One identity: AppFlowy account is created from the Authentik email; no
  second password; Bloud SSO session is reused.
- ✅ Bloud owns the provider configuration (client, issuer, mapping) and
  re-applies it every reconcile.
- ⬜ GoTrue still stores an account record — that is AppFlowy's architecture
  (the cloud API only verifies GoTrue JWTs). Disable/delete propagation via
  the gotrue admin API is a later enhancement, not part of this plan.
- ⬜ The free-license 1-seat limit is unchanged (an SSO user is still a
  seat). Multi-user needs a purchased AppFlowy Cloud license.

## Implementation phases

| Phase | Scope | Verification |
|---|---|---|
| 0 | CA + leaf generation in bootstrap; trust bundle; `./bloud setup` host-trust step | Unit tests (generate once, stable across restarts); `curl -sI https://localhost:8443` on the dev VM |
| 1 | Traefik `websecure` entrypoint + cert mount; issuer route; verify Authentik issuer over TLS (set explicit issuer mode if needed) | `https://sso.localhost:8443/.well-known/openid-configuration` returns the matching `issuer`; existing HTTP routes unchanged (fast + integration tiers) |
| 2 | AppFlowy: authentik OAuth app + gotrue OIDC provider + gotrue CA trust + metadata changes; e2e spec journey becomes the SSO flow (button click → /app); Go integration test asserts the provider exists and an SSO round-trip works | `./bloud validate --tier integration`; Playwright appflowy spec green |
| 3 | Flip `BLOUD_PUBLIC_SCHEME` default to `https`; per-app redirect-URI/WS follow-ups; e2e + test CA wiring; docs (AGENTS.md, per-app INTEGRATION.md) | Full Playwright suite (all apps) on a reset dev VM |

Phase 3 is independently deferrable: Phases 0–2 deliver AppFlowy SSO with
HTTP remaining the default public scheme; Phase 3 is the "whole project goes
HTTPS" cutover.

## Files (expected)

| File | Change |
|---|---|
| `services/host-agent/internal/config/config.go` | `BLOUD_PUBLIC_SCHEME`, TLS paths |
| `services/host-agent/internal/bootstrap` (or init-secrets) | CA/leaf/bundle generation, one-time |
| `services/host-agent/internal/appconfig/traefik.go` | `websecure` entrypoint, cert mount, TLS on routers |
| `services/host-agent/internal/orchestrator` (route generation) | `tls: true` in `apps-routes.yml` |
| `services/host-agent/cmd/host-agent/main.go` | `appSubdomainURL`/`appWSSubdomainURL` scheme-aware; `{{appCaBundlePath}}` |
| `apps/appflowy/{metadata.yaml,configurator.go,configurator_test.go,INTEGRATION.md}` | OIDC provider wiring, gotrue CA trust, SSO journey |
| `e2e/tests/appflowy.spec.ts` | SSO journey (button → /app), local sign-up demoted to fallback |
| `services/host-agent/internal/e2e/e2e_test.go` | AppFlowy SSO provider + round-trip assertions |
| `dev/lima.yaml`, `cli/backend/{lima,qemu}.go` | Host :8443 forwarding |
| `AGENTS.md` | Port table (+8443), CA trust setup note |

## Risks / open questions

- **Browser trust UX**: untrusted CA = cert warnings on every https URL.
  Mitigation: HTTP stays fully functional (dual entrypoints); `./bloud setup`
  installs the CA; the scheme default can stay `http` until Phase 3.
- **Authentik issuer over a second host**: needs the Phase 1 empirical check
  (dynamic vs explicit issuer mode). If explicit mode pins the issuer to the
  HTTP URL, the TLS route may need its own OIDC provider — still local to the
  authentik configurator.
- **Certificate rotation**: 10-year validity avoids it in practice; when it
  comes, it's a manual runbook step (new CA → re-trust → regenerate leaf),
  not an automated one.
- **Java-based apps** (Jellyfin) don't honor `SSL_CERT_FILE`; they'd need a
  truststore mount in Phase 3 if they ever fetch the issuer server-side.
  (Jellyfin uses LDAP, not OIDC — no issuer calls today.)
- **Tailnet plan interplay**: Tailscale Serve terminates TLS at the edge for
  remote traffic; a local HTTPS issuer gives remote and local the same issuer
  class and simplifies `AUTHENTIK_HOST_BROWSER` handling in the tailnet plan.
