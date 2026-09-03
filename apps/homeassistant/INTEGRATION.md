# Home Assistant Integration

## Status: In progress

Home Assistant (https://www.home-assistant.io/) is an open-source home
automation platform. Bloud installs the official Home Assistant container and
wires its SSO to Bloud's identity provider (Authentik).

- Image: `docker.io/homeassistant/home-assistant` (**must be version-pinned** —
  currently `latest`, violates AGENTS.md "pin versions")
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

- Home Assistant core has **no native OIDC support**. Its auth stack supports
  local users plus legacy mechanisms; OIDC requires a custom auth provider
  (`custom_components/…`), and `hass-oidc-auth` is the mature, maintained,
  widely-deployed one.
- It registers as a first-class HA auth provider ("Log in with …" on the HA
  login page), so sessions, long-lived tokens, and refresh all run through HA's
  own auth model — no proxy header spoofing.
- It supports exactly what Bloud needs: OIDC discovery, code flow + PKCE,
  group→role mapping (admin/user), a custom `display_name`, and optional
  welcome-screen skip (`features.default_redirect`).

### Reference docs (authoritative)

- `apps/homeassistant/haas-oidc-auth-authentik-setup.md` — upstream authentik
  provider setup (application + OAuth2/OIDC provider pair, strict redirect URI
  `https://<HA URL>/auth/oidc/callback`, signing key, discovery URL).
- `apps/homeassistant/haas-oidc-auth-config.md` — upstream YAML configuration
  reference (`auth_oidc:` keys, defaults, all options).

These two files (pulled from the upstream GitHub page) are the primary sources
for this integration.

## How it works

- The integration is dropped into the HA config dir as
  `config/custom_components/auth_oidc/` and activated from `configuration.yaml`
  under the `auth_oidc:` key (`client_id`, `discovery_url`, optional
  `client_secret`, `display_name`, `id_token_signing_alg`, `groups_scope`,
  `additional_scopes`, `claims.{display_name,username,groups}`,
  `roles.{admin,user}`, `features.*`, `network.*` — see the config doc).
- `discovery_url` = Bloud's Authentik OIDC discovery URL for the app:
  `http://sso.localhost:8080/application/o/<slug>/.well-known/openid-configuration`
  for a localhost primary; `http://<primary>/application/o/<slug>/.well-known/openid-configuration`
  otherwise. The HA container must resolve `sso.localhost`; the orchestrator
  injects the issuer extra-host for native-oidc apps (AGENTS invariant #9),
  same pattern as AFFiNE/Immich. Never hardcode the issuer — resolve it at
  runtime from the typed OIDC output.
- HA's callback URL is **`/auth/oidc/callback`** — the Authentik client's
  strict redirect URI must match it. (`metadata.yaml` currently says
  `callbackPath: /auth/callback` — wrong; fix it.)
- Login flow: HA login/welcome screen → "Log in with Bloud" → Authentik login →
  `/auth/oidc/callback` on HA → HA creates the user (or rejects per `roles`).
  With `roles.admin` configured, users in that Authentik group automatically get
  HA administrator rights; `roles.user` unset means everyone else gets the user
  role.

### Fetch the integration (fetch, don't vendor)

- **Fetch, don't vendor**: do **not** commit `hass-oidc-auth` source into
  this repo. The configurator's PreStart downloads a **pinned release** of
  `christiaangoossens/hass-oidc-auth` (its integration zip asset) from GitHub,
  verifies its **sha256** against the pinned expected value, then extracts it
  into `<appDataDir>/config/custom_components/auth_oidc/` — download to a temp
  file alongside the target, hash while streaming (`io.MultiWriter`), abort on
  mismatch, then extract into place; never leave a half-installed tree.
- Reference: **`apps/jellyfin/configurator.go`** (the Jellyfin LDAP plugin)
  implements exactly this flow — see its `downloading LDAP plugin` →
  `download returned HTTP %d` → `sha256.New()` + `io.MultiWriter` →
  `download checksum mismatch` → `zip.OpenReader` extract path. Mirror it.
- Same pinned version + matching hash → no-op; version bump → re-fetch.
- `configuration.yaml` belongs to the user. Bloud owns only the `auth_oidc:`
  block within it: merge it idempotently (create if missing, update only on
  drift, never rewrite the rest of the file, never churn formatting when
  values are unchanged — invariant #7-style determinism applies here too).
- Client ID/secret are derived deterministically by the host-agent (same
  pattern as AFFiNE's `generateOIDCBlueprint` + derived secret) so HA and the
  IdP agree without a shared store.

## Audit notes (open gaps to fix)

1. **Image not pinned** (`latest`) — pin it (AGENTS.md: pin versions).
2. **`callbackPath: /auth/callback`** → must be `/auth/oidc/callback`.
3. **`HASSIO_TOKEN: "{{appDataDir}}/token"`** — `HASSIO_TOKEN` is the HassOS
   supervisor token (an opaque string, not a file path); the plain
   home-assistant container has no supervisor. Drop it, and drop the
   `/addons` volume (HassOS convention the plain image doesn't use).
4. **`PUID`/`PGID`** — the official Home Assistant image does not honor these
   (linuxserver convention). Misleading; remove.
5. **Integration provisioning** — implement PreStart fetch + sha256 verify of
   the pinned `hass-oidc-auth` release into `custom_components/auth_oidc/`.
6. **`configuration.yaml` `auth_oidc:` block** — write/merge idempotently;
   include `display_name: "Bloud SSO"` (or similar) so the login screen reads
   well; set `roles.admin: "authentik Admins"` (or Bloud's admin group) so
   Bloud admins become HA admins; consider `features.default_redirect: true`
   so users skip the welcome screen and land on Authentik.
7. **Secrets via `!secret`** — HA's recommended pattern is
   `client_secret: !secret oidc_client_secret` + `secrets.yaml`; decide whether
   to write `secrets.yaml` (user-owned file — same merge discipline) or inline
   the derived secret. Inline is simpler; `!secret` is the documented
   convention.
8. **First-run state** — HA without a completed onboarding shows the onboarding
   flow; ensure the container's first-run doesn't collide with our injected
   config (the existing `bloud-bootstrap-admin`/`PostStart` logic needs to
   survive: verify what it actually does today).

## Files

| File | Purpose |
|------|---------|
| `apps/homeassistant/INTEGRATION.md` | This document |
| `apps/homeassistant/haas-oidc-auth-authentik-setup.md` | Upstream authentik setup reference |
| `apps/homeassistant/haas-oidc-auth-config.md` | Upstream YAML config reference |
| `apps/homeassistant/metadata.yaml` | Container definition, port, SSO strategy |
| `apps/homeassistant/configurator.go` | PreStart/PostStart reconciliation (in progress) |

## Reference patterns

- **Jellyfin LDAP plugin** (`apps/homeassistant` sibling app) — download →
  stream-hash sha256 → abort on mismatch → extract zip into place. The model
  for provisioning `hass-oidc-auth`.
- `apps/affine/` + `apps/affine/INTEGRATION.md` — native-oidc provisioning,
  derived credentials, behavioral tests.
- `apps/jellyfin/` — setup-wizard completion + SSO output pattern.
