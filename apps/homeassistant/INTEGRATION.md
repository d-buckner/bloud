# Home Assistant Integration

## Status: Implemented — pending verification

Home Assistant (https://www.home-assistant.io/) is an open-source home
automation platform. Bloud installs the official Home Assistant container and
wires its SSO to Bloud's identity provider (Authentik).

- Image: `docker.io/homeassistant/home-assistant:2026.9.0` (pinned)
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
  3. writes/merges Bloud's `auth_oidc:` block in `configuration.yaml` (markers,
     idempotent, no churn on unchanged values).
  Returns `changed=true` when anything was written → the orchestrator restarts
  the node so HA picks up the new config (HA does not hot-reload auth providers).
- **PostStart**: wait for the HTTP API, complete HA first-run onboarding
  headlessly, then verify the OIDC provider is live.
- Login flow: HA login page → "Log in with Bloud" → Authentik login →
  `/auth/oidc/callback` on HA → HA provisions the user (first login) and maps
  groups → roles.

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
- **OIDC liveness check**: `GET /auth/oidc/` must return **302** (redirect to
  the IdP). 404 → provider not registered (component missing / discovery
  failed). No token needed — works even before full setup finishes.

## Design decisions

- **Secret inline, not `!secret`**: `client_secret` is written inline in the
  managed `configuration.yaml` block. `!secret` + `secrets.yaml` is upstream's
  docs convention but adds a second user-owned file to merge; the managed block
  is already marker-managed. Known trade-off: secrets readable by anyone with
  the config dir (same as any inline HA secret).
- **Markers**: `# BEGIN/END bloud managed auth_oidc` delimit Bloud's block.
  Everything else in `configuration.yaml` is the user's and is never rewritten.
  If a user authors their *own* top-level `auth_oidc:` block outside the
  markers, YAML duplicate-key error surfaces as an install failure (limitation,
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
  `homeassistant` auth provider). It's the break-glass path (break-glass owner
  above). Full enforcement (disabling legacy) isn't possible without removing
  the only durable credential; e2e asserts SSO *works* + identity comes from
  Authentik, not that legacy is dead.

## Files

| File | Purpose |
|------|---------|
| `apps/homeassistant/INTEGRATION.md` | This document |
| `apps/homeassistant/haas-oidc-auth-authentik-setup.md` | Upstream authentik setup reference |
| `apps/homeassistant/haas-oidc-auth-config.md` | Upstream YAML config reference |
| `apps/homeassistant/metadata.yaml` | Container definition, port, SSO strategy |
| `apps/homeassistant/configurator.go` | PreStart fetch/config + PostStart onboarding/verify |
| `apps/homeassistant/configurator_test.go` | Unit tests (httptest) |
| `apps/homeassistant/icon.png` | Catalog icon |

## Reference patterns

- `apps/jellyfin/configurator.go` — `ensureLDAPPlugin`: download → stream-hash
  sha256 → abort on mismatch → extract zip (staging dir + rename). The model for
  provisioning `hass-oidc-auth`.
- `apps/affine/` + `apps/affine/INTEGRATION.md` — native-oidc provisioning,
  typed OIDC output usage, behavioral e2e.
