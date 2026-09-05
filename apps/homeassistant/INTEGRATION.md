# Home Assistant Integration

## Status: Implemented — SSO verified behind the reverse proxy (manual browser test)

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

- The configurator's **PreStart** (runs before every container start):
  1. creates `<appDataDir>/config`;
  2. fetches the pinned `hass-oidc-auth` release zip, verifies sha256, extracts
     into `<appDataDir>/config/custom_components/auth_oidc/` (see constants);
  3. writes/merges Bloud's `auth_oidc:` provider block in `configuration.yaml`
     (markers, idempotent, no churn on unchanged values);
  4. writes the reverse-proxy trust into HA's **stored http config entry**
     `<configDir>/.storage/http` (see "Reverse proxy" below).
  Returns `changed=true` when anything was written → the orchestrator restarts
  the node so HA picks up the new configuration (HA loads its HTTP/auth config at
  startup and never hot-reloads it).
- **PostStart**: wait for the HTTP API, complete HA first-run onboarding headlessly,
  then verify the OIDC provider is live.
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
  `{client_id: "https://home-assistant.io/iOS", username, password, name}` →
  creates the owner and returns an access token. The iOS client is one of HA's
  built-in OAuth2 clients, so no client registration chicken-and-egg.
- Owner account `bloud-bootstrap-admin`, password from
  `secrets.GenerateAppAdminPassword("homeassistant")` (durable in
  `secrets.json`). **The owner cannot be deleted in HA** (unlike Jellyfin's
  bootstrap admin) — it is the documented break-glass account. Users authenticate
  via SSO; the owner password is never shared.
- **OIDC liveness check**: `GET /auth/oidc/welcome` must return **200** (the view
  is registered only when `async_setup` succeeded, and discovery is part of
  setup, so 200 proves both component load and provider discovery). 404 →
  provider not registered. No token needed — works even before full setup finishes.

## Manual browser test — status and observations (manual test session, 2026-09-04)

The full product flow was validated end-to-end by hand (fresh VM):

1. **First install attempt FAILED. Second install attempt WORKED.**
   Suspected cause: the flow does not properly wait for the container image
   pull to complete before proceeding (needs confirmation — see Open items).
2. After the successful install, opening the Home Assistant app tile shows
   **HA's OIDC landing page** ("Log in with Bloud" button + "Default login"
   link). Clicking through the **two** login screens completes the OIDC login
   and **then everything worked** — the user lands signed-in on the HA dashboard.
3. The golden path is therefore PROVEN at the product level. The remaining work
   is automation only (see Open items).

## Open items (tracked)

1. **First-try install must work.** Reproduce: fresh VM → `./bloud install
   homeassistant` → fails; run again → works. Investigate the image-pull wait:
   the install path appears to proceed before the image pull has completed
   (possibly a timeout vs. an actual pull-completion check, or a status check
   that races the pull). Fix so the first install succeeds.
2. **E2E test must drive HA's OIDC landing page.** The current spec navigates to
   the tile and assumes a direct redirect; instead the browser must click the
   HA OIDC landing page buttons (two screens: "Log in with Bloud" → the
   following login screen). Use Playwright to dump the rendered document at each
   phase and derive stable selectors (roles/text) for each phase, then update the
   spec.

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
| `apps/homeassistant/configurator.go` | PreStart fetch/config + reverse-proxy + PostStart onboarding/verify |
| `apps/homeassistant/configurator_test.go` | Unit tests (httptest) |
| `apps/homeassistant/icon.png` | Catalog icon |

## Reference patterns

- `apps/jellyfin/configurator.go` — `ensureLDAPPlugin`: download → stream-hash
  sha256 → abort on mismatch → extract zip (staging dir + rename). The model for
  provisioning `hass-oidc-auth`.
- `apps/affine/` + `apps/affine/INTEGRATION.md` — native-oidc provisioning,
  typed OIDC output usage, behavioral e2e.
