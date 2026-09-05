# Home Assistant Integration

## Status: Implemented — **currently REGRESSED**: the browser goes straight to the first-run onboarding page and OIDC login is never offered (see Open items #3). Earlier verified: SSO verified behind the reverse proxy (manual browser test, 2026-09-04).

Home Assistant (https://www-home-assistant.io/) is an open-source home
automation platform. Bloud installs the official Home Assistant container and
wires its SSO to Bloud's identity provider (Authentik).

- Image: `docker.io/home-assistant/home-assistant:2026.9.0` (pinned)
- Port: 8123
- Public URL: `http://homeassistant.localhost:8080` (direct `:8123` for debugging)

## SSO approach (DECIDED — not up for debate)

**SSO is implemented with the community auth provider
[christiaangoossens/hass-oidc-auth](https://github.com/christiaangoossens/hass-oidc-auth).**

This is a settled decision. Do not revisit it and do not propose alternatives
(legacy `trusted_networks`, `trusted_devices`, other community auth hacks, a
custom fork, reverse-proxy header spoofing, etc.). If something about the SSO
flow doesn't work, debug *this* integration — don't replace it.

### Why this component

- Home Assistant core has **no native OIDC support**. OIDC requires a custom
  auth provider (`custom_components/…`), and `hass-oidc-auth` is the mature,
  maintained, widely-deployed one.
- It registers as a first-class HA auth provider ("Log in with …" on the HA
  login page), so sessions, long-lived tokens, and refresh run through HA's own
  auth model — no proxy header spoofing.
- Supports exactly what Bloud needs: OIDC discovery, code flow + PKCE,
  group→role mapping (admin/user), custom `display_name`, welcome-screen skip
  (`features.default_redirect`, default on).

### Reference docs (authoritative)

- `apps/homeassistant/haas-oidc-auth-authentik-setup.md` — upstream authentik
  provider setup (application + OAuth2/OIDC provider pair, strict redirect URI
  `/auth/oidc/callback`, signing key, discovery URL).
- `apps/homeassistant/haas-oidc-auth-config.md` — upstream YAML configuration
  reference (`auth_oidc:` keys, defaults, all options).

These two files (pulled from the upstream GitHub page) are the primary sources
for this integration. **Read them before touching the configurator.**

## How it works

The configurator's **PreStart** (runs before every container start):
  1. creates `<appDataDir>/config`;
  2. fetches the pinned `hass-oidc-auth` release zip, verifies sha256, extracts
     into `<appDataDir>/config/custom_components/auth_oidc/` (see constants);
  3. writes/merges Bloud's `auth_oidc:` provider block in `configuration.yaml`
     (markers, idempotent, no churn on unchanged values).
  Returns `changed=true` when anything was written → the orchestrator restarts
  the node so HA picks up the new configuration (HA loads its HTTP/auth config at
  startup and never hot-reloads it). The reverse-proxy trust patch is **not** done
  here — it happens in PostStart, after HA has written its stored http entry (see
  "Reverse proxy" below).
- **PostStart**: wait for the HTTP API; complete HA first-run onboarding headlessly
  (creating the owner); patch reverse-proxy trust into HA's stored http config
  entry and restart HA in-place if it changed; verify the OIDC provider is live.
- Login flow: HA login page ("Log in with Bloud" button) → Authentik login →
  `/auth/oidc/callback` on HA → HA provisions the user (first login) and maps
  groups → roles.

## Reverse proxy (required — HA behind Traefik) — VERIFIED WORKING

Traefik runs on the **host network** and reaches HA over the published loopback
port (`http://localhost:8123`), adding `X-Forwarded-*` headers on every request.
Home Assistant refuses to honour `X-Forwarded-*` unless it is explicitly told it
sits behind a reverse proxy, and its forwarded middleware then rejects the
proxied request (`homeassistant.components.http.forwarded` raises
`HTTPBadRequest`). The symptom is a **`400: Bad Request` page after the Authentik
round-trip** — the OIDC callback (`/auth/oidc/callback`) never completes. The HA
log shows `homeassistant.components.http.forwarded: A request from a reverse
proxy was received … but your HTTP integration is not set-up for reverse
proxies`.

**HA 2026.x migrated the `http:` YAML block into a stored config entry**
(`<config>/.storage/http` — the `.storage/` file keyed `http`) and ignores the
YAML `http:` block (it files a `yaml_still_present_after_migration` repair).
Writing `use_x_forwarded_for: true` under `http:` in `configuration.yaml` does
nothing on these versions — the callback still 400s.

**Fix (current code, verified working via manual browser test):**
`ensureReverseProxy` in `configurator.go` merges `use_x_forwarded_for` +
`trusted_proxies` into the stored entry's `data.stable` object while preserving
every other field HA wrote. HA (schema v2) writes this file on its own first
start, and it **rejects hand-written config entries** with strict schema
validation — a bad entry takes down the whole http integration (auth, onboarding,
everything). So the code **never creates the file**: it is a no-op until HA has
written its own entry. PostStart calls it after HA's entry exists and restarts
HA (in-container restart via the process manager) to apply it.

The stored entry HA writes looks like (v2 layout):

```json
{
  "version": 2,
  "minor_version": 2,
  "key": "http",
  "data": {
    "stable": {
      "server_port": 8123,
      "cors_allowed_origins": ["https://cast.home-assistant.io"],
      "login_attempts_threshold": -1,
      "ip_ban_enabled": true,
      "ssl_profile": "modern",
      "use_x_frame_options": true,
      "created_at": "...",
      "use_x_forwarded_for": true,
      "trusted_proxies": ["10.0.0.0/8"]
    },
    "pending": null,
    "yaml_migration_done": true
  }
}
```

Note: older notes described a v1 layout (flat `{"id","use_x_forwarded_for",
"trusted_proxies"}` under `data`). The live HA 2026.9 writes the **v2 layout
above** — the merge targets `data.stable`, and `data` without a `stable` block
is treated as "nothing to patch yet" (no-op, not an error).

`trusted_proxies` must cover the address HA's socket sees the proxy connect from.
Traefik connects to the published `localhost:8123` and podman's rootless
port-forward presents that connection from HA's container network, so HA sees a
peer in the private `10.0.0.0/8` range. The broad `/8` is a deliberate,
documented trade-off: the only thing that can reach HA's port is the container
network itself (Traefik via the published port; everything else is firewalled at
the container boundary), so this internal network is not attacker-reachable. Home
Assistant logs a warning about broad trusted proxies — that warning is expected
and accepted here. This is standard reverse-proxy configuration, not header
spoofing — the SSO decision above still holds.

## Verified constants (hass-oidc-auth — do NOT re-investigate)

Verified against upstream source at tag `v1.2.1`:

| Constant | Value |
|---|---|
| Version | `v1.2.1` |
| Asset URL | `https://github.com/christiaangoossens/hass-oidc-auth/releases/download/v1.2.1/hass-oidc-auth.zip` |
| sha256 | `e5badaaacaa63cfd6fe733924a05e76d75058836190398598fb24de57cd47ccd` |
| Zip layout | **flat** — files at zip root (`__init__.py`, `manifest.json`, …); NO top-level dir to strip. Extract *into* `custom_components/auth_oidc/`. |
| Component domain | `auth_oidc` (manifest `domain: auth_oidc`) → config key `auth_oidc:` |
| Callback route | `/auth/oidc/callback` (confirmed in source) |

### Verified upstream behavior (v1.2.1 source)

- `discovery_url` is fetched **verbatim** as the well-known URL — the
  integration does **not** append `/.well-known/openid-configuration` itself.
  Bloud must write the FULL well-known URL:
  `strings.TrimSuffix(OIDC.IssuerURL, "/") + "/.well-known/openid-configuration"`.
- The integration fetches discovery during `async_setup`; on discovery failure
  setup fails and its HTTP routes are never registered.
- YAML schema is strict (`extra=REMOVE_EXTRA`); keys: `client_id` (req),
  `discovery_url` (req), `client_secret`, `display_name`, `id_token_signing_alg`,
  `groups_scope`, `additional_scopes`, `claims.{display_name="name",
  username="preferred_username", groups="groups"}`, `roles.{admin="admins",
  user}`, `features.*`, `network.*`.
- `features.default_redirect` (skip welcome screen) defaults **on**;
  `disable_rfc7636` off; groups scope included by default.
- HA's admin group for `roles.admin` on Bloud: **`authentik Admins`** (the group
  Bloud's own service account and admin user belong to, per
  `apps/authentik/scripts/`). Authentik's `groups` claim emits group names.

## First-run onboarding (headless)

HA without a completed onboarding shows the onboarding flow for every visitor
and no auth API works. PostStart completes it headlessly (owner creation is the
only onboarding step needed):

- `GET /api/onboarding` (public pre-onboarding) → `[{"step":"user"}]` when
  incomplete, `[]` when done.
- `POST /api/onboarding/users` with
  `{client_id: "https://home-assistant.io/iOS", username, password, name, language}` →
  creates the owner and returns a **one-shot authorization code** (`{"auth_code": …}`),
  **not** an access token (verified against core `2026.9` `onboarding/views.py`,
  which has zero `access_token` in the module). The iOS client is one of HA's
  built-in OAuth2 clients, so no client registration chicken-and-egg.
- Exchange that code for a usable Bearer token: `POST /auth/token` with
  `{grant_type: "authorization_code", client_id: <same built-in id>, code}` →
  `{access_token, refresh_token, expires_in}` (core `auth/__init__.py`). HA's
  `/auth/token` supports only `authorization_code` and `refresh_token` grants —
  there is **no** password grant and no HTTP way to mint a token for an
  already-onboarded instance. HA validates only that the client id parses as an
  IndieAuth URL, so no redirect_uri/client_secret is involved. This access token
  is what drives the authenticated `homeassistant.restart` below (the service
  call requires admin).
- Owner account `bloud-bootstrap-admin`, password from
  `secrets.GenerateAppAdminPassword("homeassistant")` (durable in
  `secrets.json`). **The owner cannot be deleted in HA** (unlike Jellyfin's
  bootstrap admin) — it is the documented break-glass account. Users authenticate
  via SSO; the owner password is never shared.
- **OIDC liveness check**: `GET /auth/oidc/welcome` must return **200** (the view
  registers only when `async_setup` succeeded, and discovery is part of setup, so
  200 proves both component load and provider discovery). Non-200 → not ready yet:
  404 covers both "provider never registered" and "HA still booting", so the check
  retries until the deadline. No token needed.

## Manual browser test — status and observations (manual test session, 2026-09-04)

The full product flow was validated end-to-end by hand (fresh VM):

1. **First install attempt FAILED ("no access token to restart Home
   Assistant"); second attempt WORKED.** Root cause (rooted, not the image
   pull): see "Why a retry used to be needed" below — the onboarding code was
   never exchanged for an access token, so the post-trust restart had none. Fixed.
2. After the successful install, opening the Home Assistant app tile shows
   **HA's OIDC landing page** ("Log in with Bloud" button + "Default login"
   link). Clicking through the **two** login screens completes the OIDC login
   and **then everything worked** — the user lands signed-in on the HA dashboard.
   *(This behavior has since REGRESSED — the tile now goes directly to the
   onboarding page without offering OIDC login; see Open items #3.)*
3. The golden path was therefore PROVEN at the product level on 2026-09-04. The
   remaining work was automation only — now superseded by the #3 regression.

### Why a retry used to be needed (fixed)

Symptom on a fresh host: first `./bloud install homeassistant` errored with
**"no access token to restart Home Assistant"**; running it again succeeded. It
was **not** the image pull. The chain:

1. PostStart patches reverse-proxy trust into `.storage/http`. On a fresh HA that
   is a real change, so it triggers an in-place `homeassistant.restart` — which
   needs an admin Bearer token.
2. The owner's access token comes from onboarding, but the code parsed
   `access_token` off the onboarding response. HA never returns that key (it
   returns `{"auth_code": …}`), so the token was **always empty** → restart failed
   with "no access token".
3. Retrying "worked" because the second run's PostStart found trust **already**
   set on disk (`ensureReverseProxy` → no change) and skipped the restart entirely
   — it never fixed the token; it just avoided the step that needed one, while the
   container kept running with a HA process that had loaded its own (unpatched)
   entry. The pass itself had failed: the node landed in ERROR, which the
   orchestrator treats as terminal — nothing auto-reconciles it; only an explicit
   install intent resets errored nodes (`pipeline.resetErroredNodes`).

Fix: exchange the onboarding authorization code for a real access token (see
"First-run onboarding" above) so the restart has a token. And if no token is
available — e.g. a retry against an already-onboarded HA, which never re-issues a
code — PostStart fails the pass with a self-describing error instead of failing
opaquely: the patched entry is on disk, and the retry install resets the errored
node and recreates the container, so HA loads the trust with no token at all.

## Open items (tracked)

1. **First-try install must work — FIXED.** The fresh-host failure was
   "no access token to restart Home Assistant", **not** the image pull (the
   earlier image-pull hypothesis was wrong). Onboarding returns an authorization
   code, never an `access_token`; the old code parsed `access_token`, so the
   post-trust restart always had an empty token. Now exchanged via `/auth/token`
   (see "First-run onboarding" + "Why a retry used to be needed"). Covered by
   `TestEnsureOnboardedExchangesAuthCodeForToken` and
   `TestPostStartAlreadyOnboardedSkipsRestart`.
2. **E2E test must drive HA's OIDC landing page.** The current spec navigates to
   the tile and assumes a direct redirect; instead the browser must click the
   HA OIDC landing page buttons (two screens: "Log in with Bloud" → the
   following login screen). Use Playwright to dump the rendered document at each
   phase and derive stable selectors (roles/text) for each phase, then update the
   spec.
3. **[BUG — FIXED] The browser went straight to the first-run onboarding wizard;
   hass-oidc-auth login was never offered.** On a fresh host, opening the app tile
   no longer landed on the hass-oidc-auth landing page ("Log in with Bloud" +
   "Default login"). It went directly to the wizard, which then *always* failed
   with **"Something went wrong loading onboarding, try refreshing"**.
   Root cause (rooted against upstream `onboarding/views.py` +
   `endpoints/welcome.py`):
   - HA routes *every* unauthenticated visitor to the wizard until all wizard
     steps are closed — the sign-in page where the OIDC provider lives is
     unreachable until then. `ensureOnboarded` closed only the `user` step, so
     every post-install tile visit hit the half-open wizard (the
     "goes straight to onboarding" symptom).
   - The wizard itself then failed because its backing API calls ran before the
     backend's onboarding integration had registered its routes — HA serves wizard
     panels before the integration registers them — yielding the permanent
     "Something went wrong loading onboarding, try refreshing" loop.
   - Aggravating it: once every step is closed HA *deregisters*
     `GET /api/onboarding` (404 forever). `ensureOnboarded` treated any 404 as
     "still booting" and burned the whole postStart timeout into ERROR — and
     since PostStart re-runs each reconcile pass, a "Retry install" could never
     make the app reach `running`. Observed directly (status=error with
     "…/api/onboarding keeps failing: HTTP 404: 404: Not Found").
   Fix: `ensureOnboarded` now closes the `core_config`, `analytics` and
   `integration` wizard steps with the owner token (idempotent; 403 replays are
   success) so the browser lands directly on the provider welcome page and OIDC
   login is offered from the first visit; and it treats a permanent 404 with an
   owner present in the auth store (`ownerOnDisk`, read from `.storage/auth`
   like the http entry) as "already onboarded" instead of "still booting", so the
   retry-install path converges again.
   Verified: `BLOUD_E2E_APP=homeassistant ./bloud e2e app` — green (dashboard
   reached, signed-in identity visible).

## Design decisions

- **Secret inline, not `!secret`**: `client_secret` is written inline in the
  managed `configuration.yaml` block. `!secret` + `secrets.yaml` is upstream's
  docs convention but adds a second user-owned file to merge; the managed block
  is already marker-managed. Known trade-off: secrets readable by anyone with
  the config dir (same as any inline HA secret).
- **Markers**: `# BEGIN/END bloud managed auth_oidc` delimit Bloud's block.
  Everything else in `configuration.yaml` is the user's and is never rewritten.
  If a user authors their *own* top-level `auth_oidc:` block outside the
  markers, a YAML duplicate-key error surfaces as an install failure (limitation,
  documented).
- **No hardcoding of issuer/redirect**: everything comes from the typed
  `OIDCOutput` (`IssuerURL`, `RedirectURI` built from `sso.callbackPath`,
  host-set aware). The orchestrator injects the issuer extra-host for
  native-oidc containers (AGENTS invariant #9) so `sso.localhost` resolves
  inside HA.
- **Fetch, don't vendor**: no `hass-oidc-auth` source committed. Download to
  temp alongside target, hash while streaming (`io.MultiWriter`), abort on
  mismatch, extract to staging then rename into place — never a half-installed
  tree. Mirror `apps/jellyfin/configurator.go` (`ensureLDAPPlugin`).
- **Legacy password login stays enabled** on the HA login page (the
  `homeassistant` auth provider). It is the break-glass path (see break-glass
  owner above). Full enforcement (disabling legacy) isn't possible without
  removing the only durable credential; e2e asserts SSO *works* + identity comes
  from Authentik, not that legacy is dead.

## Files

| File | Purpose |
|------|---------|
| `apps/homeassistant/INTEGRATION.md` | This document |
| `apps/homeassistant/haas-oidc-auth-authentik-setup.md` | Upstream authentik setup reference |
| `apps/homeassistant/haas-oidc-auth-config.md` | Upstream YAML config reference |
| `apps/homeassistant/metadata.yaml` | Container definition, port, SSO strategy |
| `apps/homeassistant/configurator.go` | PreStart fetch/config; PostStart onboarding + reverse-proxy patch/restart + OIDC verify |
| `apps/homeassistant/configurator_test.go` | Unit tests (httptest) |
| `apps/homeassistant/icon.png` | Catalog icon |

## Reference patterns

- `apps/jellyfin/configurator.go` — `ensureLDAPPlugin`: download → stream-hash
  sha256 → abort on mismatch → extract zip (staging dir + rename). The model for
  provisioning `hass-oidc-auth`.
- `apps/affine/` + `apps/affine/INTEGRATION.md` — native-oidc provisioning,
  typed OIDC output usage, behavioral e2e.
